test:
	ginkgo .

coverage:
	go test -coverprofile test.cov
report: coverage
	go tool cover -html=test.cov


.PHONY: test coverage report
