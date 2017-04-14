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
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	edgegrid "github.com/akamai-open/AkamaiOPEN-edgegrid-golang"
	uuid "github.com/google/uuid"
	homedir "github.com/mitchellh/go-homedir"
)

const (
	defaultEdgerc            = "~/.edgerc"
	defaultSection           = "default"
	defaultMethod            = "invalidate"
	defaultNetwork           = "staging"
	defaultFileType          = "text"
	maxBodySize              = 50000
	cachePurgeRequestMethohd = "POST"
	retryThreshold           = float64(50) // suitable
	defaultRetryCount        = float64(0)
)

var (
	jsonOverHead     = len([]byte(`{"objects":[]}`))
	jsonLineOverHead = len([]byte(`"",`))
)

// RequestBody ...
type RequestBody struct {
	Objects []string `json:"objects"`
}

// Config is configuration for Akamai Fast Purge(CCU v3) request
type Config struct {
	edgerc            string
	section           string
	method            string
	network           string
	fileType          string
	fastPurgeEndpoint string
	edgeConf          edgegrid.Config
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
func Validation(config *Config, invalidationRequestFile string) error {

	// Validate invalidation url list file
	err := chkExist(invalidationRequestFile)
	if err != nil {
		return errors.New("specify a cache invalidation request list file. if you would like to use text format, should set \"-t text\" option. reference: https://developer.akamai.com/api/purge/ccu/data.html")
	}

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

	// Validate invalidation request url list file
	fp, err := os.Open(invalidationRequestFile)
	chkErr(err)
	defer fp.Close()

	fileinfo, err := fp.Stat()
	chkErr(err)
	if fileinfo.Size() == 0 {
		return errors.New("cache invalidation request list is empty")
	}
	if config.fileType == "json" && maxBodySize < fileinfo.Size() {
		return errors.New("cache invalidation request list is too large")
	}

	reader := bufio.NewReaderSize(fp, 4096)
	line, _, err := reader.ReadLine()
	if config.fileType == "text" {
		// ARL start S/ or L/
		// reference: https://community.akamai.com/community/web-performance/blog/2016/01/18/cache-keys-why-we-should-know-them
		if string(line[0:2]) != "S/" && string(line[0:2]) != "L/" {
			return errors.New("cache invalidation request list is invalid formatted")
		}
	}
	if config.fileType == "json" && string(line[0:1]) != "{" {
		return errors.New("cache invalidation request list is invalid formatted")
	}
	return nil
}

// Invalidation request to Akamai CCU v3 (a.k.a Fast Purge) with credential and ARL list
func Invalidation(config *Config, invalidationRequestFile string) error {
	var buffer bytes.Buffer
	var wg sync.WaitGroup

	fp, err := os.Open(invalidationRequestFile)
	chkErr(err)
	defer fp.Close()

	// Text file support
	if config.fileType == "text" {
		bufsize := maxBodySize - jsonOverHead
		scanner := bufio.NewScanner(fp)

		// Chop the text file by request body size upper limit
		// reference: https://developer.akamai.com/api/purge/ccu/overview.html#limits
		for scanner.Scan() {
			line := scanner.Bytes()
			bufsize = bufsize - len(line) - jsonLineOverHead
			if 0 < bufsize {
				buffer.Write(line)
				buffer.Write([]byte("\n"))
			} else {
				body := make([]byte, maxBodySize)
				_, err := buffer.Read(body)
				reqBody := createJSON(body)
				chkErr(err)

				// Request cache invalidation in concurrently
				wg.Add(1)
				reqID := fmt.Sprint(uuid.New())
				chkErr(err)
				go invalidationRequest(config, reqBody, &wg, defaultRetryCount, reqID)

				bufsize = maxBodySize - jsonOverHead - len(line) - jsonLineOverHead
				buffer.Reset()
				buffer.Write(line)
				buffer.Write([]byte("\n"))
			}
		}
		body := make([]byte, maxBodySize)
		_, err := buffer.Read(body)
		reqBody := createJSON(body)
		chkErr(err)

		// Request cache invalidation
		reqID := fmt.Sprint(uuid.New())
		chkErr(err)
		wg.Add(1)
		invalidationRequest(config, reqBody, &wg, defaultRetryCount, reqID)

		if err := scanner.Err(); err != nil {
			fmt.Fprintln(os.Stderr, "reading standard input:", err)
		}
	}

	// JSON file support
	if config.fileType == "json" {
		reqBody, err := ioutil.ReadAll(fp)
		chkErr(err)
		reqID := fmt.Sprint(uuid.New())
		chkErr(err)
		wg.Add(1)
		invalidationRequest(config, reqBody, &wg, defaultRetryCount, reqID)
	}

	wg.Wait()
	return err
}

func invalidationRequest(config *Config, data []byte, wg *sync.WaitGroup, count float64, reqID string) {
	defer wg.Done()

	next := make([]byte, len(data))
	copy(next, data)

	bodyBuf := bytes.NewBuffer(data)
	client := &http.Client{}
	req, err := http.NewRequest(
		cachePurgeRequestMethohd,
		fmt.Sprintf(
			"https://%s"+config.fastPurgeEndpoint+"/"+config.network,
			config.edgeConf.Host,
		),
		bodyBuf,
	)
	chkErr(err)

	// Add Akamai Authorization header
	req = edgegrid.AddRequestHeader(config.edgeConf, req)

	// Send invalidation request
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("request failed: %s\n", err)
	}
	defer resp.Body.Close()

	respBody, _ := ioutil.ReadAll(resp.Body)
	log.Printf("request_id: %s, response: %s\n", reqID, respBody)

	// Error retry with exponential backoff and full jitter
	// akamai api limits: https://developer.akamai.com/api/purge/ccu/overview.html#limits
	// exponential backoff: https://www.awsarchitectureblog.com/2015/03/backoff.html
	baseDuration := time.Duration(5)
	additionalDuration := randomTime(0, int(math.Pow(2, count)))
	duration := baseDuration + additionalDuration

	if resp.StatusCode >= http.StatusInternalServerError && retryThreshold > count {
		time.Sleep(time.Duration(duration) * time.Second)
		count++
		wg.Add(1)
		go invalidationRequest(config, next, wg, count, reqID)
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

func randomTime(min, max int) time.Duration {
	rand.Seed(time.Now().UnixNano())
	return time.Duration(rand.Intn(max-min) + min)
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

func main() {
	var config Config
	flag.StringVar(&config.edgerc, "c", defaultEdgerc, "specify a edgerc file")
	flag.StringVar(&config.section, "s", defaultSection, "specify a config section")
	flag.StringVar(&config.method, "m", defaultMethod, "specify a invalidation method(invalidate or delete)")
	flag.StringVar(&config.network, "n", defaultNetwork, "specify a target network(akamai production or staging network)")
	flag.StringVar(&config.fileType, "t", defaultFileType, "specify a invalidation list type(json or text)")
	flag.Parse()
	invalidationRequestFile, err := homedir.Expand(flag.Arg(0))
	chkErr(err)

	// Validate edgerc file
	edgercPath, err := homedir.Expand(config.edgerc)
	chkErr(err)
	err = chkExist(edgercPath)
	chkErr(err)
	config.edgerc = edgercPath

	initEdgeConfig(&config)
	err = Validation(&config, invalidationRequestFile)
	chkErr(err)

	// /ccu/v3/{invalidate|delete}/url
	config.fastPurgeEndpoint = "/ccu/v3/" + config.method + "/url"

	err = Invalidation(&config, invalidationRequestFile)
	chkErr(err)
}
