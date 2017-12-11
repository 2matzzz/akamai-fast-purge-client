package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"os"
	"strconv"
	"testing"

	edgegrid "github.com/akamai-open/AkamaiOPEN-edgegrid-golang"
)

const (
	validTextInvalidationRequestFileHTTP  = "./test/test_http.txt"
	validTextInvalidationRequestFileHTTPS = "./test/test_https.txt"
	validJSONInvalidationRequestFile      = "./test/test.json"
	validEdgercFile                       = "./test/valid-edgerc"
	invalidEdgercFile                     = "./test/invalid-edgerc"
)

var invalidInvalidationRequestFile = random()

func random() string {
	var n uint64
	binary.Read(rand.Reader, binary.LittleEndian, &n)
	return strconv.FormatUint(n, 36)
}

func TestValidation(t *testing.T) {
	texts := map[string]string{
		validTextInvalidationRequestFileHTTP:  "http",
		validTextInvalidationRequestFileHTTPS: "https",
		validJSONInvalidationRequestFile:      "{",
	}
	for k, v := range texts {
		fp, err := os.OpenFile(k, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			t.Errorf("%s", err)
		}
		defer fp.Close()

		writer := bufio.NewWriter(fp)
		_, err = writer.WriteString(v)
		if err != nil {
			t.Errorf("%s", err)
		}
		writer.Flush()
	}

	config1 := Config{
		edgerc:   validEdgercFile,
		method:   "invalidate",
		network:  "production",
		fileType: "json",
		edgeConf: edgegrid.Config{
			Host:         "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx.purge.akamaiapis.net",
			ClientToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
			ClientSecret: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
			AccessToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
		},
	}
	config2 := Config{
		edgerc:   validEdgercFile,
		method:   "invalidate",
		network:  "production",
		fileType: "text",
		edgeConf: edgegrid.Config{
			Host:         "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx.purge.akamaiapis.net",
			ClientToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
			ClientSecret: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
			AccessToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
		},
	}
	config3 := Config{
		edgerc:   validEdgercFile,
		method:   "delete",
		network:  "staging",
		fileType: "text",
		edgeConf: edgegrid.Config{
			Host:         "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx.purge.akamaiapis.net",
			ClientToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
			ClientSecret: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
			AccessToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
		},
	}
	config4 := Config{
		edgerc:   validEdgercFile,
		method:   "foo",
		network:  "staging",
		fileType: "json",
		edgeConf: edgegrid.Config{
			Host:         "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx.purge.akamaiapis.net",
			ClientToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
			ClientSecret: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
			AccessToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
		},
	}
	config5 := Config{
		edgerc:   validEdgercFile,
		method:   "invalidate",
		network:  "bar",
		fileType: "text",
		edgeConf: edgegrid.Config{
			Host:         "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx.purge.akamaiapis.net",
			ClientToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
			ClientSecret: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
			AccessToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
		},
	}
	config6 := Config{
		edgerc:   validEdgercFile,
		method:   "invalidate",
		network:  "production",
		fileType: "xml",
		edgeConf: edgegrid.Config{
			Host:         "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx.purge.akamaiapis.net",
			ClientToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
			ClientSecret: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
			AccessToken:  "akab-xxxxxxxxxxxxxxxx-xxxxxxxxxxxxxxxx",
		},
	}

	// Valid: correct method, network, fileType and json format
	err1 := Validation(&config1)
	if err1 != nil {
		t.Errorf("validation failed: %s", err1)
	}

	// Valid: correct method, network, fileType and text format (HTTP)
	err2 := Validation(&config2)
	if err2 != nil {
		t.Errorf("validation failed: %s", err2)
	}

	// Valid: correct method, network, fileType and text format (HTTPS)
	err3 := Validation(&config3)
	if err3 != nil {
		t.Errorf("something went wrong, validation should be failed but succeeded")
	}

	// Invalid: wrong method
	err4 := Validation(&config4)
	if err4 == nil {
		t.Errorf("something went wrong, validation should be failed but succeeded")
	}

	// Invalid: wrong network
	err5 := Validation(&config5)
	if err5 == nil {
		t.Errorf("something went wrong, validation should be failed but succeeded")
	}

	// Invalid: wrong fileType
	err6 := Validation(&config6)
	if err6 == nil {
		t.Errorf("something went wrong, validation should be failed but succeeded")
	}

	for k := range texts {
		_ = os.Remove(k)
	}
}
