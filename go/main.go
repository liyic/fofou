package main

import (
	"code.google.com/p/gorilla/mux"
	"code.google.com/p/gorilla/securecookie"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"oauth"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	configPath   = flag.String("config", "secrets.json", "Path to configuration file")
	httpAddr     = flag.String("addr", ":5010", "HTTP server address")
	logPath      = flag.String("log", "stdout", "where to log")
	inProduction = flag.Bool("production", false, "are we running in production")
	cookieName   = "ckie"
)

var (
	oauthClient = oauth.Client{
		TemporaryCredentialRequestURI: "https://api.twitter.com/oauth/request_token",
		ResourceOwnerAuthorizationURI: "https://api.twitter.com/oauth/authenticate",
		TokenRequestURI:               "https://api.twitter.com/oauth/access_token",
	}

	config = struct {
		TwitterOAuthCredentials *oauth.Credentials
		Forums                  []ForumConfig
		CookieAuthKeyHexStr     *string
		CookieEncrKeyHexStr     *string
	}{
		&oauthClient.Credentials,
		nil,
		nil,
		nil,
	}
	logger        *log.Logger
	cookieAuthKey []byte
	cookieEncrKey []byte
	secureCookie  *securecookie.SecureCookie

	// this is where we store information about users and translation.
	// All in one place because I expect this data to be small
	dataDir string

	staticDir = "static"

	appState = AppState{}

	tmplMain        = "main.html"
	tmplForum       = "forum.html"
	templateNames   = [...]string{tmplMain, tmplForum}
	templatePaths   = make([]string, 0)
	templates       *template.Template
	reloadTemplates = true
	alwaysLogTime   = true
)

// a static configuration of a single forum
type ForumConfig struct {
	Title   string
	SiteUrl string
	Sidebar string
	Tagline string
	DataDir string
	// we authenticate only with Twitter, this is the twitter user name
	// of the admin user
	AdminTwitterUser string
}

type User struct {
	Login string
}

type Forum struct {
	ForumConfig
}

type AppState struct {
	Users  []*User
	Forums []*Forum
}

func NewForum(config *ForumConfig) *Forum {
	forum := &Forum{ForumConfig: *config}
	logger.Printf("Created %s forum\n", forum.Title)
	return forum
}

// data dir is ../data on the server or ../apptranslatordata locally
func getDataDir() string {
	if dataDir != "" {
		return dataDir
	}
	dataDir = filepath.Join("..", "fofoudata")
	if FileExists(dataDir) {
		return dataDir
	}
	dataDir = filepath.Join("..", "..", "data")
	if FileExists(dataDir) {
		return dataDir
	}
	log.Fatal("data directory (../../data or ../fofoudata) doesn't exist")
	return ""
}

func findForum(siteUrl string) *Forum {
	for _, f := range appState.Forums {
		if f.SiteUrl == siteUrl {
			return f
		}
	}
	return nil
}

func forumAlreadyExists(siteUrl string) bool {
	return nil != findForum(siteUrl)
}

func forumInvalidField(forum *Forum) string {
	forum.Title = strings.TrimSpace(forum.Title)
	if forum.Title == "" {
		return "Title"
	}
	if forum.SiteUrl == "" {
		return "SiteUrl"
	}
	if forum.DataDir == "" {
		return "DataDir"
	}
	if forum.AdminTwitterUser == "" {
		return "AdminTwitterUser"
	}
	return ""
}

func addForum(forum *Forum) error {
	if invalidField := forumInvalidField(forum); invalidField != "" {
		return errors.New(fmt.Sprintf("Forum has invalid field '%s'", invalidField))
	}
	if forumAlreadyExists(forum.SiteUrl) {
		return errors.New("Forum already exists")
	}

	/*if err := readForumData(app); err != nil {
		return err
	}*/
	appState.Forums = append(appState.Forums, forum)
	return nil
}

func GetTemplates() *template.Template {
	if reloadTemplates || (nil == templates) {
		if 0 == len(templatePaths) {
			for _, name := range templateNames {
				templatePaths = append(templatePaths, filepath.Join("tmpl", name))
			}
		}
		templates = template.Must(template.ParseFiles(templatePaths...))
	}
	return templates
}

func isTopLevelUrl(url string) bool {
	return 0 == len(url) || "/" == url
}

func serve404(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

func serveErrorMsg(w http.ResponseWriter, msg string) {
	http.Error(w, msg, http.StatusBadRequest)
}

func userIsAdmin(f *Forum, user string) bool {
	return user == f.AdminTwitterUser
}

// readSecrets reads the configuration file from the path specified by
// the config command line flag.
func readSecrets(configFile string) error {
	b, err := ioutil.ReadFile(configFile)
	if err != nil {
		return err
	}
	err = json.Unmarshal(b, &config)
	if err != nil {
		return err
	}
	cookieAuthKey, err = hex.DecodeString(*config.CookieAuthKeyHexStr)
	if err != nil {
		return err
	}
	cookieEncrKey, err = hex.DecodeString(*config.CookieEncrKeyHexStr)
	if err != nil {
		return err
	}
	secureCookie = securecookie.New(cookieAuthKey, cookieEncrKey)
	// verify auth/encr keys are correct
	val := map[string]string{
		"foo": "bar",
	}
	_, err = secureCookie.Encode(cookieName, val)
	if err != nil {
		// for convenience, if the auth/encr keys are not set,
		// generate valid, random value for them
		auth := securecookie.GenerateRandomKey(32)
		encr := securecookie.GenerateRandomKey(32)
		fmt.Printf("auth: %s\nencr: %s\n", hex.EncodeToString(auth), hex.EncodeToString(encr))
	}
	// TODO: somehow verify twitter creds
	return err
}

func makeTimingHandler(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		startTime := time.Now()
		fn(w, r)
		duration := time.Now().Sub(startTime)
		if duration.Seconds() > 1.0 || alwaysLogTime {
			url := r.URL.Path
			if len(r.URL.RawQuery) > 0 {
				url = fmt.Sprintf("%s?%s", url, r.URL.RawQuery)
			}
			logger.Printf("'%s' took %f seconds to serve\n", url, duration.Seconds())
		}
	}
}

func main() {
	testEnumFields()
	return

	flag.Parse()

	if *inProduction {
		reloadTemplates = false
		alwaysLogTime = false
	}

	if *logPath == "stdout" {
		logger = log.New(os.Stdout, "", 0)
	} else {
		loggerFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
		if err != nil {
			log.Fatalf("Failed to open log file '%s', %s\n", *logPath, err.Error())
		}
		defer loggerFile.Close()
		logger = log.New(loggerFile, "", 0)
	}

	if err := readSecrets(*configPath); err != nil {
		log.Fatalf("Failed reading config file %s. %s\n", *configPath, err.Error())
	}

	for _, forumData := range config.Forums {
		f := NewForum(&forumData)
		if err := addForum(f); err != nil {
			log.Fatalf("Failed to add the forum: %s, err: %s\n", f.Title, err.Error())
		}
	}

	if len(appState.Forums) == 0 {
		log.Fatalf("No forums defined in secrets.json")
	}

	/*
		r.HandleFunc("/app/{appname}", makeTimingHandler(handleApp))
		r.HandleFunc("/app/{appname}/{lang}", makeTimingHandler(handleAppTranslations))
		r.HandleFunc("/user/{user}", makeTimingHandler(handleUser))
		r.HandleFunc("/edittranslation", makeTimingHandler(handleEditTranslation))
		r.HandleFunc("/downloadtranslations", makeTimingHandler(handleDownloadTranslations))
		r.HandleFunc("/uploadstrings", makeTimingHandler(handleUploadStrings))
		r.HandleFunc("/atom", makeTimingHandler(handleAtom))
	*/
	r := mux.NewRouter()
	r.HandleFunc("/", makeTimingHandler(handleMain))
	r.HandleFunc("/{forum}", makeTimingHandler(handleForum))
	http.HandleFunc("/s/", makeTimingHandler(handleStatic))

	r.HandleFunc("/oauthtwittercb", handleOauthTwitterCallback)
	r.HandleFunc("/login", handleLogin)
	r.HandleFunc("/logout", handleLogout)

	http.Handle("/", r)
	logger.Printf("Running on %s\n", *httpAddr)
	if err := http.ListenAndServe(*httpAddr, nil); err != nil {
		fmt.Printf("http.ListendAndServer() failed with %s\n", err.Error())
	}

	fmt.Printf("Exited\n")
}