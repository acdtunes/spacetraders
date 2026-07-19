package cli

import (
	"sort"
	"strings"
)

// sortShipListRowsNatural sorts rows by ship symbol in natural order, in
// place. A plain string sort puts "TORWIND-10" before "TORWIND-2" (because
// '1' < '2' lexicographically), which reads as a scrambled fleet roster.
// Natural order compares embedded digit runs numerically instead, so ships
// list in the order a human expects: TORWIND-2, TORWIND-3, ..., TORWIND-10.
func sortShipListRowsNatural(rows []shipListRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		return naturalLess(rows[i].Symbol, rows[j].Symbol)
	})
}

// naturalLess reports whether a sorts before b in natural order: runs of
// ASCII digits are compared as numbers (ignoring leading zeros), everything
// else is compared byte-by-byte.
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]

		if isASCIIDigit(ca) && isASCIIDigit(cb) {
			startI, startJ := i, j
			for i < len(a) && isASCIIDigit(a[i]) {
				i++
			}
			for j < len(b) && isASCIIDigit(b[j]) {
				j++
			}

			numA := strings.TrimLeft(a[startI:i], "0")
			numB := strings.TrimLeft(b[startJ:j], "0")

			if len(numA) != len(numB) {
				return len(numA) < len(numB)
			}
			if numA != numB {
				return numA < numB
			}
			continue
		}

		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}

	return len(a)-i < len(b)-j
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
