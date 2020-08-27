import serial
import shutil
import sys
import os
import io
import psutil
import time

def main():
    if len(sys.argv) != 3:
        print("Please specify a serial port and filename")
        exit(1)
    serial_port = sys.argv[1]
    filename = sys.argv[2]
    print("Watching {filename} and uploading board at {serial_port}".format(filename=filename, serial_port=serial_port))
    with serial.Serial(serial_port, 115200, timeout=1) as ser:
        ser_text = io.TextIOWrapper(ser, newline='\n')
        continue_reading = True
        assume_in_prompt = False
        while continue_reading:
            line = ser_text.readline().strip()
            print(">>>> {}".format(line))
            #if 'Hit any key to stop autoboot:' in line:
            if 'FEC [PRIME], usb_ether' in line:
                print("Writing control characters")
                for i in range(5):
                    ser.write(b'\r\n')
                    time.sleep(0.05)
                assume_in_prompt = True
            if '=>' in line or assume_in_prompt:
                print("found prompt")
                ser.write(b'\r\n')

                premount_paths = [ partition.mountpoint for partition in psutil.disk_partitions()]

                print("Entering USB Mass Storage Mode")
                print(ser_text.readline())
                ser.write(b'ums 0 mmc 0\r\n')
                print(ser_text.readline())
                # Find USB Device
                target_partition = None

                for _ in range(3):
                    postmount_paths = [ partition.mountpoint for partition in psutil.disk_partitions()]
                    new_paths = list(set(premount_paths) - set(postmount_paths))


                    for partition in new_paths:
                        for node in os.listdir(partition):
                            if node.endswith('-m4.dtb'):
                                target_partition = partition
                                break
                        if target_partition is not None:
                            break
                    if target_partition is not None:
                        break

                    time.sleep(500) # Wait for udev to mount this thing

                if target_partition is None:
                    print("Unable to find a target partition")
                    exit(1)
                print("Copying artifact to target partition {}".format(target_partition))

                # Copy files
                basename = os.path.basename(filename)
                shutil.copy(filename, os.path.join(target_partition, basename))
                os.sync()
                os.sync()

                print("Copy complete; exiting UMS")
                ser.write(b'\x03') # CTRL+C

                print("Requesting system boot...")
                ser.write(b'boot\r\n')
                ser.close()

if __name__ == "__main__":
    main()
