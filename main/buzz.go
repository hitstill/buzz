package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/hitstill/buzz/config"
	"github.com/hitstill/buzz/formatter"

	"github.com/alessio/shellescape"
	"github.com/jroimartin/gocui"
	"github.com/mattn/go-runewidth"
	"github.com/nsf/termbox-go"
)

const VERSION = "0.5.1-rc1"

const (
	TIMEOUT_DURATION = 5 // in seconds
	WINDOWS_OS       = "windows"
	SEARCH_PROMPT    = "search> "
)

type Request struct {
	Url             string
	Method          string
	GetParams       string
	Data            string
	Headers         string
	ResponseHeaders string
	RawResponseBody []byte
	ContentType     string
	Duration        time.Duration
	Formatter       formatter.ResponseFormatter
}

type App struct {
	viewIndex    int
	historyIndex int
	currentPopup string
	history      []*Request
	config       *config.Config
	statusLine   *StatusLine
}

var METHODS = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodDelete,
	http.MethodPatch,
	http.MethodOptions,
	http.MethodTrace,
	http.MethodConnect,
	http.MethodHead,
}

var EXPORT_FORMATS = []struct {
	name   string
	export func(r Request) []byte
}{
	{
		name:   "JSON",
		export: exportJSON,
	},
	{
		name:   "curl",
		export: exportCurl,
	},
}

const DEFAULT_METHOD = http.MethodGet

var DEFAULT_FORMATTER = &formatter.TextFormatter{}

var CLIENT = &http.Client{
	Timeout: time.Duration(TIMEOUT_DURATION * time.Second),
}

var TRANSPORT = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
}

var TLS_VERSIONS = map[string]uint16{
	"TLS1.0": tls.VersionTLS10,
	"TLS1.1": tls.VersionTLS11,
	"TLS1.2": tls.VersionTLS12,
	"TLS1.3": tls.VersionTLS13,
}

func init() {
	TRANSPORT.DisableCompression = true
	CLIENT.Transport = TRANSPORT
}

func (a *App) SubmitRequest(g *gocui.Gui, _ *gocui.View) error {
	vrb, _ := g.View(RESPONSE_BODY_VIEW)
	vrb.Clear()
	vrh, _ := g.View(RESPONSE_HEADERS_VIEW)
	vrh.Clear()
	popup(g, "Sending request..")

	var r *Request = &Request{}

	go func(g *gocui.Gui, a *App, r *Request) error {
		defer g.DeleteView(POPUP_VIEW)
		// parse url
		r.Url = getViewValue(g, URL_VIEW)
		u, err := url.Parse(r.Url)
		if err != nil {
			g.Update(func(g *gocui.Gui) error {
				vrb, _ := g.View(RESPONSE_BODY_VIEW)
				fmt.Fprintf(vrb, "URL parse error: %v", err)
				return nil
			})
			return nil
		}

		q, err := url.ParseQuery(strings.Replace(getViewValue(g, URL_PARAMS_VIEW), "\n", "&", -1))
		if err != nil {
			g.Update(func(g *gocui.Gui) error {
				vrb, _ := g.View(RESPONSE_BODY_VIEW)
				fmt.Fprintf(vrb, "Invalid GET parameters: %v", err)
				return nil
			})
			return nil
		}
		originalQuery := u.Query()
		for k, v := range q {
			for _, qp := range v {
				originalQuery.Add(k, qp)
			}
		}
		u.RawQuery = originalQuery.Encode()
		r.GetParams = u.RawQuery

		// parse method
		r.Method = getViewValue(g, REQUEST_METHOD_VIEW)

		// set headers
		headers := http.Header{}
		headers.Set("User-Agent", "")
		r.Headers = getViewValue(g, REQUEST_HEADERS_VIEW)
		for _, header := range strings.Split(r.Headers, "\n") {
			if header != "" {
				header_parts := strings.SplitN(header, ": ", 2)
				if len(header_parts) != 2 {
					g.Update(func(g *gocui.Gui) error {
						vrb, _ := g.View(RESPONSE_BODY_VIEW)
						fmt.Fprintf(vrb, "Invalid header: %v", header)
						return nil
					})
					return nil
				}
				headers.Set(header_parts[0], header_parts[1])
			}
		}

		var body io.Reader

		// parse POST/PUT/PATCH data
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			bodyStr := getViewValue(g, REQUEST_DATA_VIEW)
			r.Data = bodyStr
			if headers.Get("Content-Type") != "multipart/form-data" {
				if headers.Get("Content-Type") == "application/x-www-form-urlencoded" {
					bodyStr = strings.Replace(bodyStr, "\n", "&", -1)
				}
				body = bytes.NewBufferString(bodyStr)
			} else {
				var bodyBytes bytes.Buffer
				multiWriter := multipart.NewWriter(&bodyBytes)
				defer multiWriter.Close()
				postData, err := url.ParseQuery(strings.Replace(getViewValue(g, REQUEST_DATA_VIEW), "\n", "&", -1))
				if err != nil {
					return err
				}
				for postKey, postValues := range postData {
					for i := range postValues {
						if len([]rune(postValues[i])) > 0 && postValues[i][0] == '@' {
							file, err := os.Open(postValues[i][1:])
							if err != nil {
								g.Update(func(g *gocui.Gui) error {
									vrb, _ := g.View(RESPONSE_BODY_VIEW)
									fmt.Fprintf(vrb, "Error: %v", err)
									return nil
								})
								return err
							}
							defer file.Close()
							fw, err := multiWriter.CreateFormFile(postKey, path.Base(postValues[i][1:]))
							if err != nil {
								return err
							}
							if _, err := io.Copy(fw, file); err != nil {
								return err
							}
						} else {
							fw, err := multiWriter.CreateFormField(postKey)
							if err != nil {
								return err
							}
							if _, err := fw.Write([]byte(postValues[i])); err != nil {
								return err
							}
						}
					}
				}
				body = bytes.NewReader(bodyBytes.Bytes())
			}
		}

		// create request
		req, err := http.NewRequest(r.Method, u.String(), body)
		if err != nil {
			g.Update(func(g *gocui.Gui) error {
				vrb, _ := g.View(RESPONSE_BODY_VIEW)
				fmt.Fprintf(vrb, "Request error: %v", err)
				return nil
			})
			return nil
		}
		req.Header = headers

		// set the `Host` header
		if headers.Get("Host") != "" {
			req.Host = headers.Get("Host")
		}

		// do request
		start := time.Now()
		response, err := CLIENT.Do(req)
		r.Duration = time.Since(start)
		if err != nil {
			g.Update(func(g *gocui.Gui) error {
				vrb, _ := g.View(RESPONSE_BODY_VIEW)
				fmt.Fprintf(vrb, "Response error: %v", err)
				return nil
			})
			return nil
		}
		defer response.Body.Close()

		// extract body
		r.ContentType = response.Header.Get("Content-Type")
		if response.Header.Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(response.Body)
			if err == nil {
				defer reader.Close()
				response.Body = reader
			} else {
				g.Update(func(g *gocui.Gui) error {
					vrb, _ := g.View(RESPONSE_BODY_VIEW)
					fmt.Fprintf(vrb, "Cannot uncompress response: %v", err)
					return nil
				})
				return nil
			}
		}

		bodyBytes, err := io.ReadAll(response.Body)
		if err == nil {
			r.RawResponseBody = bodyBytes
		}

		r.Formatter = formatter.New(a.config, r.ContentType)

		// add to history
		a.history = append(a.history, r)
		a.historyIndex = len(a.history) - 1

		// render response
		g.Update(func(g *gocui.Gui) error {
			vrh, _ := g.View(RESPONSE_HEADERS_VIEW)

			a.PrintBody(g)

			// print status code
			status_color := 32
			if response.StatusCode != 200 {
				status_color = 31
			}
			header := &strings.Builder{}
			fmt.Fprintf(
				header,
				"\x1b[0;%dmHTTP/1.1 %v %v\x1b[0;0m\n",
				status_color,
				response.StatusCode,
				http.StatusText(response.StatusCode),
			)

			writeSortedHeaders(header, response.Header)

			// According to the Go documentation, the Trailer maps trailer
			// keys to values in the same format as Header
			writeSortedHeaders(header, response.Trailer)

			r.ResponseHeaders = header.String()

			fmt.Fprint(vrh, r.ResponseHeaders)
			if _, err := vrh.Line(0); err != nil {
				vrh.SetOrigin(0, 0)
			}

			return nil
		})
		return nil
	}(g, a, r)

	return nil
}

func (a *App) LoadRequest(g *gocui.Gui, loadLocation string) (err error) {
	requestJson, ioErr := os.ReadFile(loadLocation)
	if ioErr != nil {
		g.Update(func(g *gocui.Gui) error {
			vrb, _ := g.View(RESPONSE_BODY_VIEW)
			vrb.Clear()
			fmt.Fprintf(vrb, "File reading error: %v", ioErr)
			return nil
		})
		return nil
	}

	var requestMap map[string]string
	jsonErr := json.Unmarshal(requestJson, &requestMap)
	if jsonErr != nil {
		g.Update(func(g *gocui.Gui) error {
			vrb, _ := g.View(RESPONSE_BODY_VIEW)
			vrb.Clear()
			fmt.Fprintf(vrb, "JSON decoding error: %v", jsonErr)
			return nil
		})
		return nil
	}

	var v *gocui.View
	url, exists := requestMap[URL_VIEW]
	if exists {
		v, _ = g.View(URL_VIEW)
		setViewTextAndCursor(v, url)
	}

	method, exists := requestMap[REQUEST_METHOD_VIEW]
	if exists {
		v, _ = g.View(REQUEST_METHOD_VIEW)
		setViewTextAndCursor(v, method)
	}

	params, exists := requestMap[URL_PARAMS_VIEW]
	if exists {
		v, _ = g.View(URL_PARAMS_VIEW)
		setViewTextAndCursor(v, params)
	}

	data, exists := requestMap[REQUEST_DATA_VIEW]
	if exists {
		g.Update(func(g *gocui.Gui) error {
			v, _ = g.View(REQUEST_DATA_VIEW)
			v.Clear()
			fmt.Fprintf(v, "%v", data)
			return nil
		})
	}

	headers, exists := requestMap[REQUEST_HEADERS_VIEW]
	if exists {
		v, _ = g.View(REQUEST_HEADERS_VIEW)
		setViewTextAndCursor(v, headers)
	}
	return nil
}

func (a *App) LoadConfig(configPath string) error {
	if configPath == "" {
		// Load config from default path
		configPath, _ = config.GetDefaultConfigLocation()
	}

	// If the config file doesn't exist, load the default config
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		a.config = &config.DefaultConfig
		a.config.Keys = config.DefaultKeys
		a.statusLine, _ = NewStatusLine(a.config.General.StatusLine)
		return nil
	}

	conf, err := config.LoadConfig(configPath)
	if err != nil {
		a.config = &config.DefaultConfig
		a.config.Keys = config.DefaultKeys
		return err
	}

	a.config = conf
	sl, err := NewStatusLine(conf.General.StatusLine)
	if err != nil {
		a.config = &config.DefaultConfig
		a.config.Keys = config.DefaultKeys
		return err
	}
	a.statusLine = sl
	return nil
}

func (a *App) ParseArgs(g *gocui.Gui, args []string) error {
	a.Layout(g)
	g.SetCurrentView(VIEWS[a.viewIndex])
	vheader, err := g.View(REQUEST_HEADERS_VIEW)
	if err != nil {
		return errors.New("too small screen")
	}
	vheader.Clear()
	vget, _ := g.View(URL_PARAMS_VIEW)
	vget.Clear()
	content_type := ""
	set_data := false
	set_method := false
	set_binary_data := false
	arg_index := 1
	args_len := len(args)
	accept_types := make([]string, 0, 8)
	var body_data []string
	for arg_index < args_len {
		arg := args[arg_index]
		switch arg {
		case "-H", "--header":
			if arg_index == args_len-1 {
				return errors.New("no header value specified")
			}
			arg_index += 1
			header := args[arg_index]
			fmt.Fprintf(vheader, "%v\n", header)
		case "-d", "--data", "--data-binary", "--data-urlencode":
			if arg_index == args_len-1 {
				return errors.New("no POST/PUT/PATCH value specified")
			}

			arg_index += 1
			set_data = true
			set_binary_data = arg == "--data-binary"
			arg_data := args[arg_index]

			if !set_binary_data {
				content_type = "form"
			}

			if arg == "--data-urlencode" {
				arg_data = url.PathEscape(arg_data)
			}

			body_data = append(body_data, arg_data)
		case "-j", "--json":
			if arg_index == args_len-1 {
				return errors.New("no POST/PUT/PATCH value specified")
			}

			arg_index += 1
			json_str := args[arg_index]
			content_type = "json"
			accept_types = append(accept_types, config.ContentTypes["json"])
			set_data = true
			vdata, _ := g.View(REQUEST_DATA_VIEW)
			setViewTextAndCursor(vdata, json_str)
		case "-X", "--request":
			if arg_index == args_len-1 {
				return errors.New("no HTTP method specified")
			}
			arg_index++
			set_method = true
			method := args[arg_index]
			if content_type == "" && (method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch) {
				content_type = "form"
			}
			vmethod, _ := g.View(REQUEST_METHOD_VIEW)
			setViewTextAndCursor(vmethod, method)
		case "-t", "--timeout":
			if arg_index == args_len-1 {
				return errors.New("no timeout value specified")
			}
			arg_index += 1
			timeout, err := strconv.Atoi(args[arg_index])
			if err != nil || timeout <= 0 {
				return errors.New("invalid timeout value")
			}
			a.config.General.Timeout = config.Duration{Duration: time.Duration(timeout) * time.Millisecond}
		case "--compressed":
			vh, _ := g.View(REQUEST_HEADERS_VIEW)
			if !strings.Contains(getViewValue(g, REQUEST_HEADERS_VIEW), "Accept-Encoding") {
				fmt.Fprintln(vh, "Accept-Encoding: gzip, deflate")
			}
		case "-e", "--editor":
			if arg_index == args_len-1 {
				return errors.New("no timeout value specified")
			}
			arg_index += 1
			a.config.General.Editor = args[arg_index]
		case "-k", "--insecure":
			a.config.General.Insecure = true
		case "-R", "--disable-redirects":
			a.config.General.FollowRedirects = false
		case "--tlsv1.0":
			a.config.General.TLSVersionMin = tls.VersionTLS10
			a.config.General.TLSVersionMax = tls.VersionTLS10
		case "--tlsv1.1":
			a.config.General.TLSVersionMin = tls.VersionTLS11
			a.config.General.TLSVersionMax = tls.VersionTLS11
		case "--tlsv1.2":
			a.config.General.TLSVersionMin = tls.VersionTLS12
			a.config.General.TLSVersionMax = tls.VersionTLS12
		case "--tlsv1.3":
			a.config.General.TLSVersionMin = tls.VersionTLS13
			a.config.General.TLSVersionMax = tls.VersionTLS13
		case "-T", "--tls":
			if arg_index >= args_len-1 {
				return errors.New("missing TLS version range: MIN,MAX")
			}
			arg_index++
			arg := args[arg_index]
			v := strings.Split(arg, ",")
			min := v[0]
			max := min
			if len(v) > 1 {
				max = v[1]
			}
			minV, minFound := TLS_VERSIONS[min]
			if !minFound {
				return errors.New("Minimum TLS version not found: " + min)
			}
			maxV, maxFound := TLS_VERSIONS[max]
			if !maxFound {
				return errors.New("Maximum TLS version not found: " + max)
			}
			a.config.General.TLSVersionMin = minV
			a.config.General.TLSVersionMax = maxV
		case "-x", "--proxy":
			if arg_index == args_len-1 {
				return errors.New("missing proxy URL")
			}
			arg_index += 1
			u, err := url.Parse(args[arg_index])
			if err != nil {
				return fmt.Errorf("invalid proxy URL: %v", err)
			}
			switch u.Scheme {
			case "", "http", "https":
				TRANSPORT.Proxy = http.ProxyURL(u)
			case "socks5h", "socks5":
				dialer, err := proxy.FromURL(u, proxy.Direct)
				if err != nil {
					return fmt.Errorf("can't connect to proxy: %v", err)
				}
				TRANSPORT.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
			default:
				return errors.New("unknown proxy protocol")
			}
		case "-F", "--form":
			if arg_index == args_len-1 {
				return errors.New("no POST/PUT/PATCH value specified")
			}

			arg_index += 1
			form_str := args[arg_index]
			content_type = "multipart"
			set_data = true
			vdata, _ := g.View(REQUEST_DATA_VIEW)
			setViewTextAndCursor(vdata, form_str)
		case "-f", "--file":
			if arg_index == args_len-1 {
				return errors.New("-f or --file requires a file path be provided as an argument")
			}
			arg_index += 1
			loadLocation := args[arg_index]
			a.LoadRequest(g, loadLocation)
		default:
			u := args[arg_index]
			if strings.Index(u, "http://") != 0 && strings.Index(u, "https://") != 0 {
				u = fmt.Sprintf("%v://%v", a.config.General.DefaultURLScheme, u)
			}
			parsed_url, err := url.Parse(u)
			if err != nil || parsed_url.Host == "" {
				return errors.New("invalid url")
			}
			if parsed_url.Path == "" {
				parsed_url.Path = "/"
			}
			vurl, _ := g.View(URL_VIEW)
			vurl.Clear()
			for k, v := range parsed_url.Query() {
				for _, vv := range v {
					fmt.Fprintf(vget, "%v=%v\n", k, vv)
				}
			}
			parsed_url.RawQuery = ""
			setViewTextAndCursor(vurl, parsed_url.String())
		}
		arg_index += 1
	}

	if set_data && !set_method {
		vmethod, _ := g.View(REQUEST_METHOD_VIEW)
		setViewTextAndCursor(vmethod, http.MethodPost)
	}

	if !set_binary_data && content_type != "" && !a.hasHeader(g, "Content-Type") {
		fmt.Fprintf(vheader, "Content-Type: %v\n", config.ContentTypes[content_type])
	}

	if len(accept_types) > 0 && !a.hasHeader(g, "Accept") {
		fmt.Fprintf(vheader, "Accept: %v\n", strings.Join(accept_types, ","))
	}

	var merged_body_data string
	if set_data && !set_binary_data {
		merged_body_data = strings.Join(body_data, "&")
	}

	vdata, _ := g.View(REQUEST_DATA_VIEW)
	setViewTextAndCursor(vdata, merged_body_data)

	return nil
}

func (a *App) hasHeader(g *gocui.Gui, h string) bool {
	for _, header := range strings.Split(getViewValue(g, REQUEST_HEADERS_VIEW), "\n") {
		if header == "" {
			continue
		}
		header_parts := strings.SplitN(header, ": ", 2)
		if len(header_parts) != 2 {
			continue
		}
		if header_parts[0] == h {
			return true
		}
	}
	return false
}

// Apply startup config values. This is run after a.ParseArgs, so that
// args can override the provided config values
func (a *App) InitConfig() {
	CLIENT.Timeout = a.config.General.Timeout.Duration
	TRANSPORT.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: a.config.General.Insecure,
		MinVersion:         a.config.General.TLSVersionMin,
		MaxVersion:         a.config.General.TLSVersionMax,
	}
	CLIENT.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		if a.config.General.FollowRedirects {
			return nil
		}
		return http.ErrUseLastResponse
	}
}

func help() {
	fmt.Println(`buzz - Interactive cli tool for HTTP inspection

Usage: buzz [-H|--header HEADER]... [-d|--data|--data-binary DATA] [-X|--request METHOD] [-t|--timeout MSECS] [URL]

Other command line options:
  -c, --config PATH        Specify custom configuration file
  -e, --editor EDITOR      Specify external editor command
  -f, --file REQUEST       Load a previous request
  -F, --form DATA          Add multipart form request data and set related request headers
                           If the value starts with @ it will be handled as a file path for upload
  -h, --help               Show this
  -j, --json JSON          Add JSON request data and set related request headers
  -k, --insecure           Allow insecure SSL certs
  -R, --disable-redirects  Do not follow HTTP redirects
  -T, --tls MIN,MAX        Restrict allowed TLS versions (values: TLS1.0,TLS1.1,TLS1.2,TLS1.3)
                           Examples: wuzz -T TLS1.1        (TLS1.1 only)
                                     wuzz -T TLS1.0,TLS1.1 (from TLS1.0 up to TLS1.1)
  --tlsv1.0                Forces TLS1.0 only
  --tlsv1.1                Forces TLS1.1 only
  --tlsv1.2                Forces TLS1.2 only
  --tlsv1.3                Forces TLS1.3 only
  -v, --version            Display version number
  -x, --proxy URL          Set HTTP(S) or SOCKS5 proxy

Key bindings:
  ctrl+r              Send request
  ctrl+s              Save response
  ctrl+e              Save request
  ctrl+f              Load request
  tab, ctrl+j         Next window
  shift+tab, ctrl+k   Previous window
  alt+h               Show history
  pageUp              Scroll up the current window
  pageDown            Scroll down the current window`,
	)
}

func main() {
	configPath := ""
	args := os.Args
	for i, arg := range os.Args {
		switch arg {
		case "-h", "--help":
			help()
			return
		case "-v", "--version":
			fmt.Printf("buzz %v\n", VERSION)
			return
		case "-c", "--config":
			configPath = os.Args[i+1]
			args = append(os.Args[:i], os.Args[i+2:]...)
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				log.Fatal("Config file specified but does not exist: \"" + configPath + "\"")
			}
		}
	}
	var g *gocui.Gui
	var err error
	for _, outputMode := range []gocui.OutputMode{gocui.Output256, gocui.OutputNormal, gocui.OutputMode(termbox.OutputGrayscale)} {
		g, err = gocui.NewGui(outputMode)
		if err == nil {
			break
		}
	}
	if err != nil {
		log.Panicln(err)
	}

	if runtime.GOOS == WINDOWS_OS && runewidth.IsEastAsian() {
		g.ASCII = true
	}

	app := &App{history: make([]*Request, 0, 31)}

	// overwrite default editor
	defaultEditor = ViewEditor{app, g, false, gocui.DefaultEditor}

	initApp(app, g)

	// load config (must be done *before* app.ParseArgs, as arguments
	// should be able to override config values). An empty string passed
	// to LoadConfig results in LoadConfig loading the default config
	// location. If there is no config, the values in
	// config.DefaultConfig will be used.
	err = app.LoadConfig(configPath)
	if err != nil {
		g.Close()
		log.Fatalf("Error loading config file: %v", err)
	}

	err = app.ParseArgs(g, args)

	// Some of the values in the config need to have some startup
	// behavior associated with them. This is run after ParseArgs so
	// that command-line arguments can override configuration values.
	app.InitConfig()

	if err != nil {
		g.Close()
		fmt.Println("Error!", err)
		os.Exit(1)
	}

	err = app.SetKeys(g)
	if err != nil {
		g.Close()
		fmt.Println("Error!", err)
		os.Exit(1)
	}

	defer g.Close()

	if err := g.MainLoop(); err != nil && err != gocui.ErrQuit {
		log.Panicln(err)
	}
}

func exportJSON(r Request) []byte {
	requestMap := map[string]string{
		URL_VIEW:             r.Url,
		REQUEST_METHOD_VIEW:  r.Method,
		URL_PARAMS_VIEW:      r.GetParams,
		REQUEST_DATA_VIEW:    r.Data,
		REQUEST_HEADERS_VIEW: r.Headers,
	}

	request, err := json.Marshal(requestMap)
	if err != nil {
		return []byte{}
	}
	return request
}

func exportCurl(r Request) []byte {
	var headers, params string
	for _, header := range strings.Split(r.Headers, "\n") {
		if header == "" {
			continue
		}
		headers = fmt.Sprintf("%s -H %s", headers, shellescape.Quote(header))
	}
	if r.GetParams != "" {
		params = fmt.Sprintf("?%s", r.GetParams)
	}
	return []byte(fmt.Sprintf("curl %s -X %s -d %s %s\n", headers, r.Method, shellescape.Quote(r.Data), shellescape.Quote(r.Url+params)))
}
