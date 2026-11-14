package detect

import (
	"regexp"
	"strconv"
	"strings"
)

// oneConfiguration is every board Apple sold with a single Configuration:
// its CPU and GPU core counts. A board sold with several is not here, and
// its counts are read from a kernel log (CoreCounts). The counts are those
// of Apple's Tech Specs pages for each Mac in the Asahi device list.
var oneConfiguration = map[string][2]int{
	"j274":  {8, 8},   // Mac mini (M1, 2020)
	"j293":  {8, 8},   // MacBook Pro (13-inch, M1, 2020)
	"j456":  {8, 8},   // iMac (24-inch, 4-port, M1, 2021)
	"j457":  {8, 7},   // iMac (24-inch, 2-port, M1, 2021)
	"j316s": {10, 16}, // MacBook Pro (16-inch, M1 Pro, 2021)
	"j493":  {8, 10},  // MacBook Pro (13-inch, M2, 2022)
	"j473":  {8, 10},  // Mac mini (M2, 2023)
	"j416s": {12, 19}, // MacBook Pro (16-inch, M2 Pro, 2023)
	"j415":  {8, 10},  // MacBook Air (15-inch, M2, 2023)
	"j433":  {8, 8},   // iMac (24-inch, 2-port, M3, 2023)
	"j434":  {8, 10},  // iMac (24-inch, 4-port, M3, 2023)
	"j504":  {8, 10},  // MacBook Pro (14-inch, M3, Nov 2023)
	"j516s": {12, 18}, // MacBook Pro (16-inch, M3 Pro, Nov 2023)
	"j514c": {16, 40}, // MacBook Pro (14-inch, M3 Max, Nov 2023), T6031
	"j516c": {16, 40}, // MacBook Pro (16-inch, M3 Max, Nov 2023), T6031
	"j514m": {14, 30}, // MacBook Pro (14-inch, M3 Max, Nov 2023), T6034
	"j516m": {14, 30}, // MacBook Pro (16-inch, M3 Max, Nov 2023), T6034
	"j615":  {8, 10},  // MacBook Air (15-inch, M3, 2024)
	"j623":  {8, 8},   // iMac (24-inch, 2-port, M4, 2024)
	"j624":  {10, 10}, // iMac (24-inch, 4-port, M4, 2024)
	"j773g": {10, 10}, // Mac mini (M4, 2024)
	"j604":  {10, 10}, // MacBook Pro (14-inch, M4, Nov 2024)
	"j616s": {14, 20}, // MacBook Pro (16-inch, M4 Pro, Nov 2024)
	"j715":  {10, 10}, // MacBook Air (15-inch, M4, 2025)
	"j700":  {6, 5},   // MacBook Neo (A18 Pro, 2026)
	"j704":  {10, 10}, // MacBook Pro (14-inch, M5, 2025)
	"j716s": {18, 20}, // MacBook Pro (16-inch, M5 Pro, 2026)
	"j815":  {10, 10}, // MacBook Air (15-inch, M5, 2026)
	"j873g": {12, 12}, // Mac mini (M6, 2026)
}

// OneConfiguration is the CPU and GPU core counts of a board Apple sold
// with one Configuration; ok is false for any other board.
func OneConfiguration(board string) (cpu, gpu int, ok bool) {
	c, ok := oneConfiguration[board]
	return c[0], c[1], ok
}

// Board is a Mac's board, from its device tree's compatible list (board,
// then SoC): "j314s" for "apple,j314s". It is "" for a list that names no
// Apple board.
func Board(compatible []string) string {
	if len(compatible) == 0 {
		return ""
	}
	board, ok := strings.CutPrefix(compatible[0], "apple,")
	if !ok {
		return ""
	}
	return board
}

var (
	reCPUs     = regexp.MustCompile(`(?m)\bsmp: Brought up \d+ nodes?, (\d+) CPUs?\s*$`)
	reGPUCores = regexp.MustCompile(`(?m)\basahi [0-9a-f]+\.gpu:\s+Active cores: (\d+)\s*$`)
)

// CoreCounts reads a Mac's CPU and GPU core counts from the kernel log of
// one boot. A count the log does not carry is 0: the GPU's line is there
// only where the asahi driver is bound.
func CoreCounts(kernelLog string) (cpu, gpu int) {
	if m := reCPUs.FindStringSubmatch(kernelLog); m != nil {
		cpu, _ = strconv.Atoi(m[1])
	}
	if m := reGPUCores.FindStringSubmatch(kernelLog); m != nil {
		gpu, _ = strconv.Atoi(m[1])
	}
	return cpu, gpu
}
