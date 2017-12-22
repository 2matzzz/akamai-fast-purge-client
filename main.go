package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"sync"
	"time"

	edgegrid "github.com/akamai-open/AkamaiOPEN-edgegrid-golang"
	uuid "github.com/google/uuid"
	homedir "github.com/mitchellh/go-homedir"
	"github.com/sirupsen/logrus"
)

const (
	defaultEdgerc            = "~/.edgerc"
	defaultSection           = "default"
	defaultMethod            = "invalidate"
	defaultNetwork           = "staging"
	defaultFileType          = "text"
	defaultLogLevel          = "error"
	maxBodySize              = 50000
	cachePurgeRequestMethohd = "POST"
	retryThreshold           = 10 // uint32 shifting
	defaultRetryCount        = 0
)

var (
	jsonOverHead     = len([]byte(`{"objects":[]}`))
	jsonLineOverHead = len([]byte(`"",`))
	log              = logrus.New()
	logLevel         logrus.Level
)

// RequestBody ...
type RequestBody struct {
	Objects []string `json:"objects"`
}

// Config is configuration for Akamai Fast Purge(CCU v3) request
type Config struct {
	edgerc   string
	section  string
	method   string
	network  string
	fileType string
	logLevel string
	edgeConf edgegrid.Config
}

func chkExist(path string) error {
	if len(path) == 0 {
		return errors.New("specify a file path")
	}
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return err
	}
	fp, err := os.Open(path)
	chkErr(err)
	defer fp.Close()
	return nil
}

// Validation check args provided to client. If args has invalid parameter(s), Validation returns error
func Validation(config *Config) error {

	// Validate edgerc params
	if len(config.edgeConf.Host) == 0 {
		return errors.New("edgerc does not have \"host\" parameter")
	}
	if len(config.edgeConf.ClientToken) == 0 {
		return errors.New("edgerc does not have \"client_token\" parameter")
	}
	if len(config.edgeConf.ClientSecret) == 0 {
		return errors.New("edgerc does not have \"client_secret\" parameter")
	}
	if len(config.edgeConf.AccessToken) == 0 {
		return errors.New("edgerc does not have \"access_token\" parameter")
	}

	// Validate config params
	if config.method != "invalidate" && config.method != "delete" {
		return errors.New("you should specify a invalidation method is \"invalidate\" or \"delete\"")
	}
	if config.network != "production" && config.network != "staging" {
		return errors.New("you should specify a invalidation network is \"production\" or \"staging\"")
	}
	if config.fileType != "json" && config.fileType != "text" {
		return errors.New("you should specify a cache invalidation request list type is \"json\" or \"text\"")
	}
	return nil
}

// InvalidateByURLs ...
func InvalidateByURLs(config *Config, fp io.Reader, wg *sync.WaitGroup) (err error) {
	var buffer bytes.Buffer
	bufsize := maxBodySize - jsonOverHead
	scanner := bufio.NewScanner(fp)

	// Chop the text file by request body size upper limit
	// reference: https://developer.akamai.com/api/purge/ccu/overview.html#limits
	for scanner.Scan() {
		line := scanner.Bytes()
		_, err := url.Parse(string(line))
		chkErr(err)
		bufsize = bufsize - len(line) - jsonLineOverHead
		if 0 < bufsize {
			buffer.Write(line)
			buffer.Write([]byte("\n"))
		} else {
			body := make([]byte, maxBodySize)
			_, err := buffer.Read(body)
			reqBody := createJSON(body)
			chkErr(err)
			wg.Add(1)
			go invalidationRequest(config, reqBody, wg)

			bufsize = maxBodySize - jsonOverHead - len(line) - jsonLineOverHead
			buffer.Reset()
			buffer.Write(line)
			buffer.Write([]byte("\n"))
		}
	}
	body := make([]byte, maxBodySize)
	count, err := buffer.Read(body)
	chkErr(err)
	reqBody := createJSON(body[:count])

	// Request cache invalidation
	wg.Add(1)
	go invalidationRequest(config, reqBody, wg)

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "reading standard input:", err)
	}
	return err
}

// InvalidateByBodies ...
func InvalidateByBodies(config *Config, fp io.Reader, wg *sync.WaitGroup) (err error) {
	dec := json.NewDecoder(fp)
	for {
		var reqBody = map[string]interface{}{}
		if err = dec.Decode(&reqBody); err != nil {
			if err == io.EOF {
				err = nil
			}
			break
		}
		var bodyBuf []byte
		if bodyBuf, err = json.Marshal(reqBody); err != nil {
			break
		}
		wg.Add(1)
		go invalidationRequest(config, bodyBuf, wg)
	}
	return err
}

// Invalidation request to Akamai CCU v3 (a.k.a Fast Purge) with credential and URL list
func Invalidation(config *Config, in io.Reader) (err error) {
	var wg sync.WaitGroup

	switch config.fileType {
	case "text":
		err = InvalidateByURLs(config, in, &wg)
	case "json":
		err = InvalidateByBodies(config, in, &wg)
	}

	wg.Wait()
	return err
}

func buildRequestURL(config *Config) *url.URL {
	return &url.URL{
		Scheme: "https",
		Host:   config.edgeConf.Host,
		Path:   path.Join("/ccu/v3", config.method, "url", config.network),
	}
}

const (
	baseDuration = 5
)

// Error retry with exponential backoff and full jitter
// akamai api limits: https://developer.akamai.com/api/purge/ccu/overview.html#limits
// exponential backoff: https://www.awsarchitectureblog.com/2015/03/backoff.html
// This is "Full Jitter" algorithm
func nextDelay(count int) time.Duration {
	var tmp int64 = 1 << uint32(count) * baseDuration
	return time.Duration(tmp/2+rand.Int63n(tmp/2)) * time.Second
}

func invalidationRequest(config *Config, data []byte, wg *sync.WaitGroup) {
	defer wg.Done()
	reqID := uuid.New().String()

L:
	for i := 0; i < retryThreshold; i++ {
		bodyBuf := bytes.NewBuffer(data)
		client := &http.Client{}
		req, err := http.NewRequest(cachePurgeRequestMethohd, buildRequestURL(config).String(), bodyBuf)
		chkErr(err)

		// Add Akamai Authorization header
		req = edgegrid.AddRequestHeader(config.edgeConf, req)

		// Send invalidation request
		if resp, err := client.Do(req); err == nil {
			respBody, _ := ioutil.ReadAll(resp.Body)
			resp.Body.Close()

			switch resp.StatusCode {
			case http.StatusTooManyRequests, http.StatusInsufficientStorage:
				log.Printf("[Rate limited]request_id: %s\n", reqID)
			case http.StatusCreated:
				log.Printf("[Succeed]request_id: %s, response: %s\n", reqID, respBody)
				break L
			default:
				log.Errorf("[Failed]request_id: %s, request_body_length: %d, response_status: %d, response_body: %s, request_header: %s, request_body: %s, \n", reqID, req.ContentLength, resp.StatusCode, string(respBody), req.Header["Authorization"], string(data))
				break L
			}
		}
		// Don't delay at last iteration
		if retryThreshold-i > 1 {
			time.Sleep(nextDelay(i))
		}
	}
}

func createJSON(data []byte) (body []byte) {
	buf := bytes.NewBuffer(data)
	rb, err := createRequestBody(buf)
	chkErr(err)
	body, err = json.Marshal(rb)
	chkErr(err)
	return body
}

func createRequestBody(in io.Reader) (RequestBody, error) {
	r := bufio.NewReader(in)
	rb := RequestBody{
		Objects: []string{},
	}
	for {
		l, err := r.ReadString(byte('\n'))
		if err != nil {
			if err == io.EOF {
				break
			}
			return rb, err
		}
		rb.Objects = append(rb.Objects, l[:len(l)-1])
	}
	return rb, nil
}

func chkErr(err error) {
	if err != nil {
		log.Fatalf("%s", err)
	}
}

func initEdgeConfig(config *Config) {
	// Akamai library using panic in casually... :(
	defer func() {
		if err := recover(); err != nil {
			return
		}
	}()
	config.edgeConf = edgegrid.InitConfig(config.edgerc, config.section)
}

func setLogLevel(config *Config) (err error) {
	logLevel, err = logrus.ParseLevel(config.logLevel)
	logrus.SetLevel(logLevel)
	return err
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func main() {
	var config Config
	flag.StringVar(&config.edgerc, "c", defaultEdgerc, "specify a edgerc file")
	flag.StringVar(&config.section, "s", defaultSection, "specify a config section")
	flag.StringVar(&config.method, "m", defaultMethod, "specify a invalidation method(invalidate or delete)")
	flag.StringVar(&config.network, "n", defaultNetwork, "specify a target network(akamai production or staging network)")
	flag.StringVar(&config.fileType, "t", defaultFileType, "specify a invalidation list type(json or text)")
	flag.StringVar(&config.logLevel, "l", defaultLogLevel, "specify log level(info or debug)")
	flag.Parse()

	err := setLogLevel(&config)
	chkErr(err)

	// Validate edgerc file
	edgercPath, err := homedir.Expand(config.edgerc)
	chkErr(err)
	err = chkExist(edgercPath)
	chkErr(err)
	config.edgerc = edgercPath

	initEdgeConfig(&config)

	err = Validation(&config)
	chkErr(err)

	if flag.NArg() == 0 {
		err = Invalidation(&config, os.Stdin)
	} else {
		for i := 0; i < flag.NArg(); i++ {
			invalidationRequestFile, err := homedir.Expand(flag.Arg(i))
			chkErr(err)
			in, err := os.Open(invalidationRequestFile)
			chkErr(err)
			err = Invalidation(&config, in)
			in.Close()
			chkErr(err)
		}
	}
	chkErr(err)
}
