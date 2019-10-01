module github.com/mattermost/mmetl

go 1.12

require (
	github.com/mattermost/mattermost-server v0.0.0-20190816150046-3f905844dfda
	github.com/pkg/errors v0.8.1
	github.com/spf13/cobra v0.0.3
	github.com/stretchr/testify v1.3.0
	golang.org/x/tools v0.0.0-20191001123449-8b695b21ef34 // indirect
)

replace (
	git.apache.org/thrift.git => github.com/apache/thrift v0.0.0-20180902110319-2566ecd5d999
	// Workaround for https://github.com/golang/go/issues/30831 and fallout.
	github.com/golang/lint => github.com/golang/lint v0.0.0-20190227174305-8f45f776aaf1
)
