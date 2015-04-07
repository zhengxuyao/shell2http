/*
Executing shell commands via simple http server.
Settings through 2 command line arguments, path and shell command.
By default bind to :8080.

Install/update:
	go get -u github.com/msoap/shell2http
	ln -s $GOPATH/bin/shell2http ~/bin/shell2http

Usage:
	shell2http [options] /path "shell command" /path2 "shell command2" ...
	options:
		-host="host" : host for http server, default - all interfaces
		-port=NNNN   : port for http server, default - 8080
		-form        : parse query into environment vars
		-cgi         : set some CGI variables in environment
		-no-index    : dont generate index page
		-add-exit    : add /exit command
		-log=filename: log filename, default - STDOUT
		-version
		-help

Examples:
	shell2http /top "top -l 1 | head -10"
	shell2http /date date /ps "ps aux"
	shell2http /env 'printenv | sort' /env/path 'echo $PATH' /env/gopath 'echo $GOPATH'
	shell2http /shell_vars_json 'perl -MJSON -E "say to_json(\%ENV)"'
	shell2http /cal_html 'echo "<html><body><h1>Calendar</h1>Date: <b>$(date)</b><br><pre>$(cal $(date +%Y))</pre></body></html>"'
	shell2http -form /form 'echo $v_from, $v_to'
	shell2http -cgi /user_agent 'echo $HTTP_USER_AGENT'

More complex examples:

test slow connection
	# http://localhost:8080/slow?duration=10
	shell2http -form /slow 'sleep ${v_duration:-1}; echo "sleep ${v_duration:-1} seconds"'

remote sound volume control (Mac OS)
	shell2http \
		/get  'osascript -e "output volume of (get volume settings)"' \
		/up   'osascript -e "set volume output volume (($(osascript -e "output volume of (get volume settings)")+10))"' \
		/down 'osascript -e "set volume output volume (($(osascript -e "output volume of (get volume settings)")-10))"'

remote control for Vox.app player (Mac OS)
	shell2http \
		/play_pause 'osascript -e "tell application \"Vox\" to playpause" && echo ok' \
		/get_info 'osascript -e "tell application \"Vox\"" -e "\"Artist: \" & artist & \"\n\" & \"Album: \" & album & \"\n\" & \"Track: \" & track" -e "end tell"'

get four random OS X wallpapers
	shell2http \
		/img 'cat "$(ls "/Library/Desktop Pictures/"*.jpg | ruby -e "puts STDIN.readlines.shuffle[0]")"' \
		/wallpapers 'echo "<html><h3>OS X Wallpapers</h3>"; seq 4 | xargs -I@ echo "<img src=/img?@ width=500>"'
*/
package main

import (
	"flag"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// version
const VERSION = 1.2

// default port for http-server
const PORT = 8080

// ------------------------------------------------------------------
const INDEX_HTML = `
<!DOCTYPE html>
<html>
<head>
	<title>shell2http</title>
</head>
<body>
	<h1>shell2http</h1>
	<ul>
		%s
	</ul>
	Get from: <a href="https://github.com/msoap/shell2http">github.com/msoap/shell2http</a>
</body>
</html>
`

// ------------------------------------------------------------------
// one command type
type command struct {
	path string
	cmd  string
}

// ------------------------------------------------------------------
// config struct
type config struct {
	host     string // server host
	port     int    // server port
	set_cgi  bool   // set CGI variables
	set_form bool   // parse form from URL
	no_index bool   // dont generate index page
	add_exit bool   // add /exit command
}

// ------------------------------------------------------------------
// parse arguments
func get_config() (cmd_handlers []command, app_config config, err error) {
	var log_filename string
	flag.StringVar(&log_filename, "log", "", "log filename, default - STDOUT")
	flag.IntVar(&app_config.port, "port", PORT, "port for http server")
	flag.StringVar(&app_config.host, "host", "", "host for http server")
	flag.BoolVar(&app_config.set_cgi, "cgi", false, "set some CGI variables in environment")
	flag.BoolVar(&app_config.set_form, "form", false, "parse query into environment vars")
	flag.BoolVar(&app_config.no_index, "no-index", false, "dont generate index page")
	flag.BoolVar(&app_config.add_exit, "add-exit", false, "add /exit command")
	flag.Usage = func() {
		fmt.Printf("usage: %s [options] /path \"shell command\" /path2 \"shell command2\"\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(0)
	}
	version := flag.Bool("version", false, "get version")
	flag.Parse()
	if *version {
		fmt.Println(VERSION)
		os.Exit(0)
	}

	// setup log file
	if len(log_filename) > 0 {
		fh_log, err := os.OpenFile(log_filename, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("error opening log file: %v", err)
		}
		log.SetOutput(fh_log)
	}

	// need >= 2 arguments and count of it must be even
	args := flag.Args()
	if len(args) < 2 || len(args)%2 == 1 {
		return nil, config{}, fmt.Errorf("error: need pairs of path and shell command")
	}

	for i := 0; i < len(args); i += 2 {
		path, cmd := args[i], args[i+1]
		if path[0] != '/' {
			return nil, config{}, fmt.Errorf("error: path %s dont starts with /", path)
		}
		cmd_handlers = append(cmd_handlers, command{path: path, cmd: cmd})
	}

	return cmd_handlers, app_config, nil
}

// ------------------------------------------------------------------
// setup http handlers
func setup_handlers(cmd_handlers []command, app_config config) {
	index_li_html := ""
	for _, row := range cmd_handlers {
		path, cmd := row.path, row.cmd
		shell_handler := func(rw http.ResponseWriter, req *http.Request) {
			log.Println("GET", path)

			os_exec_command := exec.Command("sh", "-c", cmd)

			proxy_system_env(os_exec_command)
			if app_config.set_form {
				get_form(os_exec_command, req)
			}
			if app_config.set_cgi {
				set_cgi_env(os_exec_command, req, app_config)
			}

			os_exec_command.Stderr = os.Stderr
			shell_out, err := os_exec_command.Output()

			if err != nil {
				log.Println("exec error: ", err)
				fmt.Fprint(rw, "exec error: ", err)
			} else {
				fmt.Fprint(rw, string(shell_out))
			}

			return
		}

		http.HandleFunc(path, shell_handler)

		log.Printf("register: %s (%s)\n", path, cmd)
		index_li_html += fmt.Sprintf(`<li><a href="%s">%s</a> <span style="color: #888">- %s<span></li>`, path, path, html.EscapeString(cmd))
	}

	// --------------
	if app_config.add_exit {
		http.HandleFunc("/exit", func(rw http.ResponseWriter, req *http.Request) {
			log.Println("GET /exit")
			fmt.Fprint(rw, "Bye...")
			go os.Exit(0)

			return
		})

		log.Printf("register: %s (%s)\n", "/exit", "/exit")
		index_li_html += fmt.Sprintf(`<li><a href="%s">%s</a></li>`, "/exit", "/exit")
	}

	// --------------
	if !app_config.no_index {
		index_html := fmt.Sprintf(INDEX_HTML, index_li_html)
		http.HandleFunc("/", func(rw http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/" {
				log.Println("404")
				http.NotFound(rw, req)
				return
			}
			log.Println("GET /")
			fmt.Fprint(rw, index_html)

			return
		})
	}
}

// ------------------------------------------------------------------
// set some CGI variables
func set_cgi_env(cmd *exec.Cmd, req *http.Request, app_config config) {
	// set HTTP_* variables
	for header_name, header_value := range req.Header {
		env_name := strings.ToUpper(strings.Replace(header_name, "-", "_", -1))
		cmd.Env = append(cmd.Env, fmt.Sprintf("HTTP_%s=%s", env_name, header_value[0]))
	}

	remote_addr := strings.Split(req.RemoteAddr, ":")
	CGI_vars := [...]struct {
		cgi_name, value string
	}{
		{"PATH_INFO", req.URL.Path},
		{"QUERY_STRING", req.URL.RawQuery},
		{"REMOTE_ADDR", remote_addr[0]},
		{"REMOTE_PORT", remote_addr[1]},
		{"REQUEST_METHOD", req.Method},
		{"REQUEST_URI", req.RequestURI},
		{"SCRIPT_NAME", req.URL.Path},
		{"SERVER_NAME", app_config.host},
		{"SERVER_PORT", fmt.Sprintf("%d", app_config.port)},
		{"SERVER_PROTOCOL", req.Proto},
		{"SERVER_SOFTWARE", "shell2http"},
	}

	for _, row := range CGI_vars {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", row.cgi_name, row.value))
	}
}

// ------------------------------------------------------------------
// parse form into environment vars
func get_form(cmd *exec.Cmd, req *http.Request) {
	err := req.ParseForm()
	if err != nil {
		log.Println(err)
		return
	}

	for key, value := range req.Form {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", "v_"+key, strings.Join(value, ",")))
	}
}

// ------------------------------------------------------------------
// proxy some system vars
func proxy_system_env(cmd *exec.Cmd) {
	for _, env_raw := range os.Environ() {
		env := strings.SplitN(env_raw, "=", 2)
		for _, env_var_name := range [...]string{"PATH", "HOME", "LANG", "USER", "TMPDIR"} {
			if env[0] == env_var_name {
				cmd.Env = append(cmd.Env, env_raw)
			}
		}
	}
}

// ------------------------------------------------------------------
func main() {
	cmd_handlers, app_config, err := get_config()
	if err != nil {
		log.Fatal(err)
	}
	setup_handlers(cmd_handlers, app_config)

	adress := fmt.Sprintf("%s:%d", app_config.host, app_config.port)
	log.Printf("listen http://%s/\n", adress)
	err = http.ListenAndServe(adress, nil)
	if err != nil {
		log.Fatal(err)
	}
}
