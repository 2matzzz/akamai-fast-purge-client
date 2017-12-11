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
	"net/url"
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
			go invalidationRequest(config, reqBody, wg, defaultRetryCount, uuid.New().String())

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
	go invalidationRequest(config, reqBody, wg, defaultRetryCount, uuid.New().String())

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
		go invalidationRequest(config, bodyBuf, wg, defaultRetryCount, uuid.New().String())
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
		invalidationRequest(config, next, wg, count, reqID)
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

	// Validate edgerc file
	edgercPath, err := homedir.Expand(config.edgerc)
	chkErr(err)
	err = chkExist(edgercPath)
	chkErr(err)
	config.edgerc = edgercPath

	initEdgeConfig(&config)

	err = Validation(&config)
	chkErr(err)

	// /ccu/v3/{invalidate|delete}/url
	config.fastPurgeEndpoint = "/ccu/v3/" + config.method + "/url"

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
