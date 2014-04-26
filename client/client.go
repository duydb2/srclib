package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/go-querystring/query"
	muxpkg "github.com/sqs/mux"
	"sourcegraph.com/sourcegraph/api_router"
	"sourcegraph.com/sourcegraph/config2"
)

const (
	libraryVersion = "0.0.1"
	userAgent      = "sourcegraph-client/" + libraryVersion
)

// A Client communicates with the Sourcegraph API.
type Client struct {
	// Services used to communicate with different parts of the Sourcegraph API.
	DocPages       DocPagesService
	Builds         BuildsService
	People         PeopleService
	BuildData      BuildDataService
	Repositories   RepositoriesService
	RepositoryTree RepositoryTreeService
	Search         SearchService
	Symbols        SymbolsService

	// Base URL for API requests, which should have a trailing slash.
	BaseURL *url.URL

	// Router used to generate URLs for the Sourcegraph API.
	apiRouter *muxpkg.Router

	// User agent used for HTTP requests to the Sourcegraph API.
	UserAgent string

	// HTTP client used to communicate with the Sourcegraph API.
	httpClient *http.Client
}

// NewClient returns a new Sourcegraph API client. If httpClient is nil,
// http.DefaultClient is used.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		cloned := *http.DefaultClient
		httpClient = &cloned
	}

	c := new(Client)
	c.httpClient = httpClient
	c.DocPages = &docPagesService{c}
	c.Builds = &buildsService{c}
	c.People = &peopleService{c}
	c.BuildData = &buildDataService{c}
	c.Repositories = &repositoriesService{c}
	c.RepositoryTree = &repositoryTreeService{c}
	c.Search = &searchService{c}
	c.Symbols = &symbolsService{c}

	c.BaseURL = config2.BaseAPIURL
	c.apiRouter = api_router.NewAPIRouter("")

	c.UserAgent = userAgent

	return c
}

// url generates the URL to the named Sourcegraph API endpoint, using the
// specified route variables and query options.
func (c *Client) url(apiRouteName string, routeVars map[string]string, opt interface{}) (*url.URL, error) {
	route := c.apiRouter.Get(apiRouteName)
	if route == nil {
		return nil, fmt.Errorf("no API route named %q", apiRouteName)
	}

	routeVarsList := make([]string, 2*len(routeVars))
	i := 0
	for name, val := range routeVars {
		routeVarsList[i*2] = name
		routeVarsList[i*2+1] = val
		i++
	}
	url, err := route.URL(routeVarsList...)
	if err != nil {
		return nil, err
	}

	// make the route URL path relative to BaseURL by trimming the leading "/"
	url.Path = strings.TrimPrefix(url.Path, "/")

	if opt != nil {
		err = addOptions(url, opt)
		if err != nil {
			return nil, err
		}
	}

	return url, nil
}

// NewRequest creates an API request. A relative URL can be provided in urlStr,
// in which case it is resolved relative to the BaseURL of the Client. Relative
// URLs should always be specified without a preceding slash. If specified, the
// value pointed to by body is JSON encoded and included as the request body.
func (c *Client) NewRequest(method, urlStr string, body interface{}) (*http.Request, error) {
	rel, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}

	u := c.BaseURL.ResolveReference(rel)

	buf := new(bytes.Buffer)
	if body != nil {
		err := json.NewEncoder(buf).Encode(body)
		if err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequest(method, u.String(), buf)
	if err != nil {
		return nil, err
	}

	req.Header.Add("User-Agent", c.UserAgent)
	return req, nil
}

// newResponse creates a new Response for the provided http.Response.
func newResponse(r *http.Response) *Response {
	response := &Response{Response: r}
	response.populatePageValues()
	return response
}

// Response is a wrapped HTTP response from the Sourcegraph API with additional
// Sourcegraph-specific response information parsed out.
type Response struct {
	*http.Response

	TotalCount int
}

func (r *Response) populatePageValues() {
	countStr := r.Header.Get("x-paging-total")
	if countStr == "" {
		return
	}

	totalCount, err := strconv.Atoi(countStr)
	if err != nil {
		return
	}

	r.TotalCount = totalCount
}

// ListOptions specifies general pagination options for fetching a list of
// results.
type ListOptions struct {
	PerPage int `url:",omitempty"`
	Page    int `url:",omitempty"`
}

const DefaultPerPage = 10

func (o ListOptions) page() int {
	if o.Page <= 0 {
		return 1
	}
	return o.Page
}

func (o ListOptions) perPage() int {
	if o.PerPage <= 0 {
		return DefaultPerPage
	}
	return o.PerPage
}

// Limit implements api_common.ResultSlice.
func (o ListOptions) Limit() int { return o.perPage() }

// Offset returns the 0-indexed offset of the first item that appears on this
// page, based on the PerPage and Page values (which are given default values if
// they are zero).
func (o ListOptions) Offset() int {
	return (o.page() - 1) * o.perPage()
}

// Do sends an API request and returns the API response.  The API response is
// decoded and stored in the value pointed to by v, or returned as an error if
// an API error has occurred.
func (c *Client) Do(req *http.Request, v interface{}) (*Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	response := newResponse(resp)

	err = CheckResponse(resp)
	if err != nil {
		// even though there was an error, we still return the response
		// in case the caller wants to inspect it further
		return response, err
	}

	if v != nil {
		if bp, ok := v.(*[]byte); ok {
			*bp, err = ioutil.ReadAll(resp.Body)
		} else {
			err = json.NewDecoder(resp.Body).Decode(v)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("error reading response from %s %s: %s", req.Method, req.URL.RequestURI(), err)
	}
	return response, nil
}

// addOptions adds the parameters in opt as URL query parameters to u. opt
// must be a struct whose fields may contain "url" tags.
func addOptions(u *url.URL, opt interface{}) error {
	v := reflect.ValueOf(opt)
	if v.Kind() == reflect.Ptr && v.IsNil() {
		return nil
	}

	qs, err := query.Values(opt)
	if err != nil {
		return err
	}

	u.RawQuery = qs.Encode()
	return nil
}

// NewMockClient returns a mockable Client for use in tests.
func NewMockClient() *Client {
	return &Client{
		DocPages:       &MockDocPagesService{},
		Builds:         &MockBuildsService{},
		People:         &MockPeopleService{},
		BuildData:      &MockBuildDataService{},
		Repositories:   &MockRepositoriesService{},
		RepositoryTree: &MockRepositoryTreeService{},
		Search:         &MockSearchService{},
		Symbols:        &MockSymbolsService{},
	}
}