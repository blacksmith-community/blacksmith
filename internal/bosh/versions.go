package bosh

import "strings"

// CompareVersions compares two release or stemcell versions part by part.
// Each part compares by its leading number first, so that 1.10 sorts above
// 1.9 and 10 above 9+dev, and then by whatever follows the number, so that
// the ordering stays consistent for parts such as 1a. A part with no leading
// number sorts below one with a number. It returns a positive number when
// left is newer, a negative one when right is newer, and zero when they
// are the same.
func CompareVersions(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")

	for part := 0; part < len(leftParts) && part < len(rightParts); part++ {
		result := compareVersionParts(leftParts[part], rightParts[part])
		if result != 0 {
			return result
		}
	}

	return len(leftParts) - len(rightParts)
}

// compareVersionParts compares one part of two versions by its leading
// number and then by its suffix. The numbers compare as digit strings with
// their leading zeros dropped, so no part is too long to compare.
func compareVersionParts(left, right string) int {
	leftNumber, leftSuffix := splitVersionPart(left)
	rightNumber, rightSuffix := splitVersionPart(right)

	switch {
	case (leftNumber == "") != (rightNumber == ""):
		if leftNumber == "" {
			return -1
		}

		return 1
	case len(leftNumber) != len(rightNumber):
		return len(leftNumber) - len(rightNumber)
	}

	result := strings.Compare(leftNumber, rightNumber)
	if result != 0 {
		return result
	}

	return strings.Compare(leftSuffix, rightSuffix)
}

// splitVersionPart splits a version part into its leading digits, without
// leading zeros, and the rest. A part of zeros alone keeps a single "0".
func splitVersionPart(part string) (string, string) {
	digits := 0
	for digits < len(part) && part[digits] >= '0' && part[digits] <= '9' {
		digits++
	}

	number := strings.TrimLeft(part[:digits], "0")
	if number == "" && digits > 0 {
		number = "0"
	}

	return number, part[digits:]
}
