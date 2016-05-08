package services

type Service struct {
	Info  string
	Plans []Plan
}

// Plan store plan info
type Plan struct {
	Manifest string
	Info     string
}
