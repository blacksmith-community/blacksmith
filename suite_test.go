package main

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

func TestBlacksmith(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Blacksmith Tests")
}
