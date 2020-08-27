# i.MX8M[M] Coprocessor Loader

Utility to automatically update boot files on a board such as the [Boundary Devices Nitrogen8M](https://boundarydevices.com/product/nitrogen8m/) or its relatives.

There are two versions:
- Go: Primary version
- Python (in `py/`): First version, a bit rougher. Just included for reference.


## Usage:
Here's a 1-liner
```
$ go build -o bin/loader ./main.go && sudo ./bin/loader /dev/ttyUSB1 /path/to/firmware/imxcm4.bin -v -f
```
