package main

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/apex/log"
	"github.com/apex/log/handlers/cli"
	"github.com/tarm/serial"
	"gopkg.in/alecthomas/kingpin.v2"
)

type Action string

const (
	ActionStartUMS       Action = "start_ums"
	ActionStopUMS        Action = "stop_ums"
	ActionTransferFile   Action = "transfer_file"
	ActionVerifyTransfer Action = "verify_transfer"
	ActionUnmountDisk    Action = "unmount_disk"
	ActionBootBoard      Action = "reset_board"
)

type Mount struct {
	Device     string
	Path       string
	Filesystem string
	Flags      string
}

func Mounts() ([]Mount, error) {
	file, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Warn(err.Error())
		}
	}()
	mounts := []Mount(nil)
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				return mounts, nil
			}
			return nil, err
		}
		if isPrefix {
			return nil, syscall.EIO
		}
		parts := strings.SplitN(string(line), " ", 5)
		if len(parts) != 5 {
			return nil, syscall.EIO
		}
		mounts = append(mounts, Mount{parts[0], parts[1], parts[2], parts[3]})
	}
}

func Copy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Close()
}

var (
	verboseArg      = kingpin.Flag("verbose", "Verbose mode.").Short('v').Bool()
	forceArg        = kingpin.Flag("force", "Force-stop UMS after copy verification.").Short('f').Bool()
	serialPortArg   = kingpin.Arg("port", "Serial port").Required().String()
	artifactPathArg = kingpin.Arg("artifact", "Path to M4 artifact").Required().String()
)

func main() {
	log.SetHandler(cli.New(os.Stdout))
	kingpin.Parse()
	if serialPortArg == nil || artifactPathArg == nil {
		panic("must specify port and artifact")
	}
	verbose := verboseArg != nil && *verboseArg == true
	forceUnmount := forceArg != nil && *forceArg == true
	serialPort := *serialPortArg
	artifactPath := *artifactPathArg

	log.Infof("Writing %s to i.MX Board with control port at %s", artifactPath, serialPort)

	artifactFile, err := os.Open(artifactPath)
	if err != nil {
		log.Fatalf("Failed to read artifact file contents: %+v", err)
	}
	defer artifactFile.Close()

	h := sha256.New()
	if _, err := io.Copy(h, artifactFile); err != nil {
		log.Fatal(err.Error())
	}

	artifactFileHash := fmt.Sprintf("%x", h.Sum(nil))
	log.Infof("Artifact sha256: %s", artifactFileHash)

	c := &serial.Config{Name: serialPort, Baud: 115200}
	s, err := serial.OpenPort(c)
	if err != nil {
		log.Fatalf("Failed to open serial port %+v", err)
	}

	actionChan := make(chan Action, 10)

	go func(actions chan Action) {
		scanner := bufio.NewScanner(s)
		for scanner.Scan() {
			line := scanner.Text()
			if verbose {
				log.Infof(">>>%s", line)
			}
			if strings.Contains(line, "FEC [PRIME], usb_ether") {
				go func() {
					time.Sleep(time.Second)
					s.Write([]byte(" "))
					actions <- ActionStartUMS
				}()
			}
			if strings.Contains(line, "UMS: LUN 0, dev 0, hwpart 0, sector 0x0") {
				log.Info("UMS Ready")
				actions <- ActionTransferFile
			}
		}
	}(actionChan)

	targetDisk := ""
	premountPaths := []Mount{}

	for {
		select {
		case action := <-actionChan:
			// Handle Action
			switch action {
			case ActionStartUMS:
				log.Info("Starting UMS")
				// TODO: Wrap writes for better logging visibility
				premountPaths, err = Mounts()
				if err != nil {
					log.Fatalf("Failed to list paths: %+v", err)
				}
				s.Write([]byte("ums 0 mmc 0\r\n"))
			case ActionTransferFile:
				log.Info("Starting File Transfer")
				newPaths := []string{}
				for i := 0; i < 10; i++ {
					postmountPaths, err := Mounts()
					if err != nil {
						log.Fatalf("Failed to list paths: %+v", err)
					}
					for _, postPath := range postmountPaths {
						inPre := false
						for _, prePath := range premountPaths {
							if prePath.Path == postPath.Path {
								inPre = true
								break
							}
						}
						if inPre {
							continue
						}
						newPaths = append(newPaths, postPath.Path)
					}
					if len(newPaths) > 0 {
						break
					}
					time.Sleep(time.Second)
				}
				if len(newPaths) < 1 {
					log.Fatal("Failed to find any new mount points")
				}

				for _, diskPath := range newPaths {
					diskContents, err := ioutil.ReadDir(diskPath)
					if err != nil {
						log.Warnf("failed to get contents of disk %s: %+v", diskPath, err)
						continue
					}
					for _, info := range diskContents {
						if strings.HasSuffix(info.Name(), "-m4.dtb") {
							targetDisk = diskPath
							break
						}
					}
					if targetDisk != "" {
						break
					}
				}

				if targetDisk == "" {
					log.Fatal("unable to find any eligible disks")
				}
				log.Infof("target disk: %s", targetDisk)

				err := Copy(artifactPath, path.Join(targetDisk, path.Base(artifactPath)))
				if err != nil {
					log.Fatalf("Failed top copy: %+v", err)
				}
				actionChan <- ActionVerifyTransfer
			case ActionVerifyTransfer:
				syscall.Sync()
				syscall.Sync()

				copiedFilePath := path.Join(targetDisk, path.Base(artifactPath))
				copiedFile, err := os.Open(copiedFilePath)
				if err != nil {
					log.Fatalf("Failed to read copied artifact file contents: %+v", err)
				}
				defer copiedFile.Close()

				h := sha256.New()
				if _, err := io.Copy(h, copiedFile); err != nil {
					log.Fatal(err.Error())
				}

				copiedFileHash := fmt.Sprintf("%x", h.Sum(nil))
				log.Infof("Artifact sha256: %s", copiedFileHash)
				if copiedFileHash != artifactFileHash {
					log.Fatal("Hash mismatch between original and copied files!")
				}
				actionChan <- ActionUnmountDisk
			case ActionUnmountDisk:
				succeeded := false
				for i := 0; i < 10; i++ {
					cmd := exec.Command("umount", targetDisk)
					err := cmd.Run()
					if err != nil {
						log.Warnf("Failed to unmount disk %s: %+v", targetDisk, err)
						time.Sleep(time.Second + (time.Second * time.Duration(i)))
						if forceUnmount {
							succeeded = true
							break
						}
						continue
					}
					succeeded = true
					break
				}
				if !succeeded {
					log.Errorf("Failed to unmount disk %s", targetDisk)
				}
				actionChan <- ActionStopUMS
			case ActionStopUMS:
				s.Write([]byte{0x03}) // CTRL+C
				time.Sleep(time.Millisecond * 100)
				actionChan <- ActionBootBoard
			case ActionBootBoard:
				s.Write([]byte("boot\r\n")) // Wrong thing
			}
		case <-time.After(time.Hour):
			log.Info("I'm tired; cleaning up!")
		}
	}
}
