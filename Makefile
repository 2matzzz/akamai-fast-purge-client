TARGET=akamai-fast-purge-client
GOVERSION=$(shell go version)
GOOS=$(word 1,$(subst /, ,$(lastword $(GOVERSION))))
GOARCH=$(word 2,$(subst /, ,$(lastword $(GOVERSION))))

.PHONY: build xbuild ${TARGET}_$(GOOS)_$(GOARCH)$(SUFFIX) clean prepare

${TARGET}:
	go build -o bin/$@

build: ${TARGET}_$(GOOS)_$(GOARCH)$(SUFFIX)

xbuild: build-windows-amd64 build-windows-386 build-linux-amd64 build-darwin-amd64

build-windows-amd64:
	@$(MAKE) build GOOS=windows GOARCH=amd64 SUFFIX=.exe

build-windows-386:
	@$(MAKE) build GOOS=windows GOARCH=386 SUFFIX=.exe

build-linux-amd64:
	@$(MAKE) build GOOS=linux GOARCH=amd64

build-darwin-amd64:
	@$(MAKE) build GOOS=darwin GOARCH=amd64

${TARGET}_$(GOOS)_$(GOARCH)$(SUFFIX):
	go build -o bin/${TARGET}_$(GOOS)_$(GOARCH)$(SUFFIX)

clean:
	rm -rf ${TARGET} ${TARGET}_*_*

prepare:
	glide install
