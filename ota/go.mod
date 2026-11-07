module github.com/labeth/open-sds800xhd/ota

go 1.26.10

require (
	github.com/labeth/open-dso-core v0.0.0
	github.com/labeth/open-sds800xhd/app v0.0.0
)

replace github.com/labeth/open-dso-core => ../../open-dso-core

replace github.com/labeth/open-sds800xhd/app => ../app
