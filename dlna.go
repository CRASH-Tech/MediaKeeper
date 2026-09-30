package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	dlnaDeviceType = "urn:schemas-upnp-org:device:MediaServer:1"
	dlnaCDType     = "urn:schemas-upnp-org:service:ContentDirectory:1"
	dlnaCMType     = "urn:schemas-upnp-org:service:ConnectionManager:1"
	dlnaMRType     = "urn:microsoft.com:service:X_MS_MediaReceiverRegistrar:1"

	// Byte seeking is supported, the content is not transcoded, and it is
	// streamed in the background transfer mode too.
	dlnaFeatures = "DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"

	dlnaRescanAfter = 30 * time.Second
)

var dlnaMime = map[string]string{
	".mkv": "video/x-matroska", ".avi": "video/x-msvideo", ".mp4": "video/mp4", ".m4v": "video/mp4",
	".mov": "video/quicktime", ".wmv": "video/x-ms-wmv", ".mpg": "video/mpeg", ".mpeg": "video/mpeg",
	".ts": "video/mp2t", ".m2ts": "video/mp2t", ".webm": "video/webm", ".flv": "video/x-flv",
}

// DLNAServer publishes a directory as a UPnP/DLNA media server: TVs and
// players on the local network find it by themselves (SSDP), browse it
// (ContentDirectory over SOAP) and play the files over HTTP.
type DLNAServer struct {
	root, name, uuid string
	port             int
	log              func(format string, args ...any)
	prober           *prober

	mu       sync.Mutex
	lib      *dlnaLibrary
	scanned  time.Time
	updateID int
}

func NewDLNAServer(root, name string, port int, log func(string, ...any)) (*DLNAServer, error) {
	return newDLNAServer(root, name, port, log, newProber())
}

func newDLNAServer(root, name string, port int, log func(string, ...any), p *prober) (*DLNAServer, error) {
	host, _ := os.Hostname()
	sum := sha1.Sum([]byte("mediakeeper|" + host + "|" + root))
	s := &DLNAServer{root: root, name: name, port: port, log: log, prober: p, updateID: 1,
		// Stable across restarts, so that clients recognize the server.
		uuid: fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])}
	if _, err := s.library(); err != nil {
		return nil, err
	}
	return s, nil
}

// library returns the current tree, rescanning the directory when the last
// scan is old: new files show up without restarting the server.
func (s *DLNAServer) library() (*dlnaLibrary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lib != nil && time.Since(s.scanned) < dlnaRescanAfter {
		return s.lib, nil
	}
	lib, err := buildLibrary(s.root)
	if err != nil {
		if s.lib != nil {
			return s.lib, nil // keep serving what is known
		}
		return nil, err
	}
	if s.lib != nil && s.lib.signature != lib.signature {
		s.updateID++
	}
	s.lib, s.scanned = lib, time.Now()
	for _, n := range lib.nodes {
		if n.IsItem() {
			s.prober.request(n.Path, n.Size, n.ModTime)
		}
	}
	return lib, nil
}

func (s *DLNAServer) serverHeader() string {
	return runtime.GOOS + "/1.0 UPnP/1.0 DLNADOC/1.50 MediaKeeper/1.0"
}

func (s *DLNAServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rootDesc.xml", s.handleDescription)
	mux.HandleFunc("/scpd/", s.handleSCPD)
	mux.HandleFunc("/ctl/", s.handleControl)
	mux.HandleFunc("/evt/", s.handleEvents)
	mux.HandleFunc("/media/", s.handleMedia)
	mux.HandleFunc("/art/", s.handleArt)
	mux.HandleFunc("/sub/", s.handleSubtitles)
	mux.HandleFunc("/", s.handleIndex)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", s.serverHeader())
		mux.ServeHTTP(w, r)
	})
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func writeXML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	io.WriteString(w, xml.Header+body)
}

func (s *DLNAServer) handleDescription(w http.ResponseWriter, r *http.Request) {
	service := func(typ, id, name string) string {
		return fmt.Sprintf("<service><serviceType>%s</serviceType><serviceId>%s</serviceId>"+
			"<SCPDURL>/scpd/%s.xml</SCPDURL><controlURL>/ctl/%s</controlURL><eventSubURL>/evt/%s</eventSubURL></service>",
			typ, id, name, name, name)
	}
	writeXML(w, `<root xmlns="urn:schemas-upnp-org:device-1-0" xmlns:dlna="urn:schemas-dlna-org:device-1-0">`+
		`<specVersion><major>1</major><minor>0</minor></specVersion><device>`+
		`<deviceType>`+dlnaDeviceType+`</deviceType>`+
		`<friendlyName>`+xmlEsc(s.name)+`</friendlyName>`+
		`<manufacturer>MediaKeeper</manufacturer><modelName>MediaKeeper</modelName>`+
		`<modelDescription>MediaKeeper DLNA media server</modelDescription><modelNumber>1</modelNumber>`+
		`<UDN>uuid:`+s.uuid+`</UDN><dlna:X_DLNADOC>DMS-1.50</dlna:X_DLNADOC>`+
		`<presentationURL>/</presentationURL><serviceList>`+
		service(dlnaCDType, "urn:upnp-org:serviceId:ContentDirectory", "cd")+
		service(dlnaCMType, "urn:upnp-org:serviceId:ConnectionManager", "cm")+
		service(dlnaMRType, "urn:microsoft.com:serviceId:X_MS_MediaReceiverRegistrar", "mr")+
		`</serviceList></device></root>`)
}

// Service descriptions: the actions a client may call and their arguments.
type scpdArg struct{ name, dir, state string }

type scpdAction struct {
	name string
	args []scpdArg
}

type scpdVar struct {
	name, typ string
	events    bool
}

var browseArgs = []scpdArg{
	{"Filter", "in", "A_ARG_TYPE_Filter"}, {"StartingIndex", "in", "A_ARG_TYPE_Index"},
	{"RequestedCount", "in", "A_ARG_TYPE_Count"}, {"SortCriteria", "in", "A_ARG_TYPE_SortCriteria"},
	{"Result", "out", "A_ARG_TYPE_Result"}, {"NumberReturned", "out", "A_ARG_TYPE_Count"},
	{"TotalMatches", "out", "A_ARG_TYPE_Count"}, {"UpdateID", "out", "A_ARG_TYPE_UpdateID"},
}

var scpds = map[string]struct {
	actions []scpdAction
	vars    []scpdVar
}{
	"cd": {
		[]scpdAction{
			{"GetSearchCapabilities", []scpdArg{{"SearchCaps", "out", "SearchCapabilities"}}},
			{"GetSortCapabilities", []scpdArg{{"SortCaps", "out", "SortCapabilities"}}},
			{"GetSystemUpdateID", []scpdArg{{"Id", "out", "SystemUpdateID"}}},
			{"Browse", append([]scpdArg{{"ObjectID", "in", "A_ARG_TYPE_ObjectID"}, {"BrowseFlag", "in", "A_ARG_TYPE_BrowseFlag"}}, browseArgs...)},
			{"Search", append([]scpdArg{{"ContainerID", "in", "A_ARG_TYPE_ObjectID"}, {"SearchCriteria", "in", "A_ARG_TYPE_SearchCriteria"}}, browseArgs...)},
		},
		[]scpdVar{
			{"SearchCapabilities", "string", false}, {"SortCapabilities", "string", false}, {"SystemUpdateID", "ui4", true},
			{"A_ARG_TYPE_ObjectID", "string", false}, {"A_ARG_TYPE_BrowseFlag", "string", false},
			{"A_ARG_TYPE_SearchCriteria", "string", false}, {"A_ARG_TYPE_Filter", "string", false},
			{"A_ARG_TYPE_Index", "ui4", false}, {"A_ARG_TYPE_Count", "ui4", false},
			{"A_ARG_TYPE_SortCriteria", "string", false}, {"A_ARG_TYPE_Result", "string", false},
			{"A_ARG_TYPE_UpdateID", "ui4", false},
		},
	},
	"cm": {
		[]scpdAction{
			{"GetProtocolInfo", []scpdArg{{"Source", "out", "SourceProtocolInfo"}, {"Sink", "out", "SinkProtocolInfo"}}},
			{"GetCurrentConnectionIDs", []scpdArg{{"ConnectionIDs", "out", "CurrentConnectionIDs"}}},
			{"GetCurrentConnectionInfo", []scpdArg{
				{"ConnectionID", "in", "A_ARG_TYPE_ConnectionID"}, {"RcsID", "out", "A_ARG_TYPE_RcsID"},
				{"AVTransportID", "out", "A_ARG_TYPE_AVTransportID"}, {"ProtocolInfo", "out", "A_ARG_TYPE_ProtocolInfo"},
				{"PeerConnectionManager", "out", "A_ARG_TYPE_ConnectionManager"}, {"PeerConnectionID", "out", "A_ARG_TYPE_ConnectionID"},
				{"Direction", "out", "A_ARG_TYPE_Direction"}, {"Status", "out", "A_ARG_TYPE_ConnectionStatus"}}},
		},
		[]scpdVar{
			{"SourceProtocolInfo", "string", true}, {"SinkProtocolInfo", "string", true}, {"CurrentConnectionIDs", "string", true},
			{"A_ARG_TYPE_ConnectionStatus", "string", false}, {"A_ARG_TYPE_ConnectionManager", "string", false},
			{"A_ARG_TYPE_Direction", "string", false}, {"A_ARG_TYPE_ProtocolInfo", "string", false},
			{"A_ARG_TYPE_ConnectionID", "i4", false}, {"A_ARG_TYPE_AVTransportID", "i4", false}, {"A_ARG_TYPE_RcsID", "i4", false},
		},
	},
	"mr": {
		[]scpdAction{
			{"IsAuthorized", []scpdArg{{"DeviceID", "in", "A_ARG_TYPE_DeviceID"}, {"Result", "out", "A_ARG_TYPE_Result"}}},
			{"IsValidated", []scpdArg{{"DeviceID", "in", "A_ARG_TYPE_DeviceID"}, {"Result", "out", "A_ARG_TYPE_Result"}}},
		},
		[]scpdVar{{"A_ARG_TYPE_DeviceID", "string", false}, {"A_ARG_TYPE_Result", "int", false}},
	},
}

func (s *DLNAServer) handleSCPD(w http.ResponseWriter, r *http.Request) {
	d, ok := scpds[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/scpd/"), ".xml")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	b.WriteString(`<scpd xmlns="urn:schemas-upnp-org:service-1-0"><specVersion><major>1</major><minor>0</minor></specVersion><actionList>`)
	for _, a := range d.actions {
		fmt.Fprintf(&b, "<action><name>%s</name><argumentList>", a.name)
		for _, arg := range a.args {
			fmt.Fprintf(&b, "<argument><name>%s</name><direction>%s</direction><relatedStateVariable>%s</relatedStateVariable></argument>",
				arg.name, arg.dir, arg.state)
		}
		b.WriteString("</argumentList></action>")
	}
	b.WriteString("</actionList><serviceStateTable>")
	for _, v := range d.vars {
		events := "no"
		if v.events {
			events = "yes"
		}
		fmt.Fprintf(&b, `<stateVariable sendEvents="%s"><name>%s</name><dataType>%s</dataType></stateVariable>`, events, v.name, v.typ)
	}
	b.WriteString("</serviceStateTable></scpd>")
	writeXML(w, b.String())
}

// handleEvents accepts subscriptions so that strict clients are satisfied.
// No events are ever sent: clients notice changes by SystemUpdateID when
// they browse.
func (s *DLNAServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "SUBSCRIBE":
		sid := r.Header.Get("SID")
		if sid == "" {
			sid = fmt.Sprintf("uuid:%s-%d", s.uuid, time.Now().UnixNano())
		}
		w.Header().Set("SID", sid)
		w.Header().Set("TIMEOUT", "Second-1800")
	case "UNSUBSCRIBE":
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// parseSOAP extracts the action name and its arguments from a request.
func parseSOAP(body io.Reader) (action string, args map[string]string, err error) {
	dec := xml.NewDecoder(body)
	args = map[string]string{}
	inBody := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", nil, errors.New("malformed SOAP request")
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case !inBody:
			inBody = start.Name.Local == "Body"
		case action == "":
			action = start.Name.Local
			// The arguments are the children of the action element.
			for {
				tok, err := dec.Token()
				if err != nil {
					return "", nil, errors.New("malformed SOAP request")
				}
				switch el := tok.(type) {
				case xml.StartElement:
					var value string
					if err := dec.DecodeElement(&value, &el); err != nil {
						return "", nil, errors.New("malformed SOAP request")
					}
					args[el.Name.Local] = value
				case xml.EndElement:
					return action, args, nil
				}
			}
		}
	}
}

func soapReply(w http.ResponseWriter, serviceType, action string, out [][2]string) {
	var b strings.Builder
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	fmt.Fprintf(&b, `<u:%sResponse xmlns:u="%s">`, action, serviceType)
	for _, kv := range out {
		fmt.Fprintf(&b, "<%s>%s</%s>", kv[0], xmlEsc(kv[1]), kv[0])
	}
	fmt.Fprintf(&b, "</u:%sResponse></s:Body></s:Envelope>", action)
	w.Header().Set("EXT", "")
	writeXML(w, b.String())
}

func soapFault(w http.ResponseWriter, code int, text string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, xml.Header+`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><s:Fault>`+
		`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>`+
		`<UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode><errorDescription>%s</errorDescription></UPnPError>`+
		`</detail></s:Fault></s:Body></s:Envelope>`, code, xmlEsc(text))
}

func (s *DLNAServer) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	action, args, err := parseSOAP(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		soapFault(w, 402, err.Error())
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/ctl/") + "#" + action {
	case "cd#Browse":
		s.browse(w, r, args, action)
	case "cd#Search":
		// Criteria are not interpreted: every video below the container is
		// returned, which is what clients asking for "all videos" expect.
		args["ObjectID"], args["BrowseFlag"] = args["ContainerID"], "Search"
		s.browse(w, r, args, action)
	case "cd#GetSystemUpdateID":
		s.library()
		s.mu.Lock()
		id := s.updateID
		s.mu.Unlock()
		soapReply(w, dlnaCDType, action, [][2]string{{"Id", strconv.Itoa(id)}})
	case "cd#GetSearchCapabilities":
		soapReply(w, dlnaCDType, action, [][2]string{{"SearchCaps", ""}})
	case "cd#GetSortCapabilities":
		soapReply(w, dlnaCDType, action, [][2]string{{"SortCaps", ""}})
	case "cm#GetProtocolInfo":
		var source []string
		seen := map[string]bool{}
		for _, mime := range dlnaMime {
			if !seen[mime] {
				seen[mime] = true
				source = append(source, "http-get:*:"+mime+":*")
			}
		}
		soapReply(w, dlnaCMType, action, [][2]string{{"Source", strings.Join(source, ",")}, {"Sink", ""}})
	case "cm#GetCurrentConnectionIDs":
		soapReply(w, dlnaCMType, action, [][2]string{{"ConnectionIDs", "0"}})
	case "cm#GetCurrentConnectionInfo":
		soapReply(w, dlnaCMType, action, [][2]string{{"RcsID", "-1"}, {"AVTransportID", "-1"}, {"ProtocolInfo", ""},
			{"PeerConnectionManager", ""}, {"PeerConnectionID", "-1"}, {"Direction", "Output"}, {"Status", "OK"}})
	case "mr#IsAuthorized", "mr#IsValidated":
		soapReply(w, dlnaMRType, action, [][2]string{{"Result", "1"}})
	default:
		soapFault(w, 401, "Invalid Action")
	}
}

func (s *DLNAServer) browse(w http.ResponseWriter, r *http.Request, args map[string]string, action string) {
	lib, err := s.library()
	if err != nil {
		soapFault(w, 501, "Action Failed")
		return
	}
	node := lib.nodes[args["ObjectID"]]
	if node == nil {
		soapFault(w, 701, "No such object")
		return
	}
	var list []*dlnaNode
	switch args["BrowseFlag"] {
	case "BrowseMetadata":
		list = []*dlnaNode{node}
	case "BrowseDirectChildren":
		list = node.Children
	case "Search":
		// From the root everything would come twice: once from the
		// catalogue, once from "Folders".
		if node.ID == dlnaRootID {
			list = append(lib.nodes[dlnaMoviesID].items(), lib.nodes[dlnaSeriesID].items()...)
		} else {
			list = node.items()
		}
	default:
		soapFault(w, 402, "Invalid Args")
		return
	}
	total := len(list)
	start, _ := strconv.Atoi(args["StartingIndex"])
	count, _ := strconv.Atoi(args["RequestedCount"])
	start = min(max(start, 0), total)
	if count <= 0 || start+count > total {
		count = total - start
	}
	list = list[start : start+count]

	base := "http://" + r.Host
	samsung := strings.Contains(r.UserAgent(), "SEC_HHP") || strings.Contains(r.UserAgent(), "Samsung")
	var b strings.Builder
	b.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
		`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/" xmlns:dlna="urn:schemas-dlna-org:metadata-1-0/" xmlns:sec="http://www.sec.co.kr/">`)
	for _, n := range list {
		s.writeDIDL(&b, n, base, samsung)
	}
	b.WriteString("</DIDL-Lite>")

	s.mu.Lock()
	updateID := s.updateID
	s.mu.Unlock()
	soapReply(w, dlnaCDType, action, [][2]string{{"Result", b.String()}, {"NumberReturned", strconv.Itoa(len(list))},
		{"TotalMatches", strconv.Itoa(total)}, {"UpdateID", strconv.Itoa(updateID)}})
}

func mimeOf(path string, samsung bool) string {
	ext := strings.ToLower(filepath.Ext(path))
	if samsung && ext == ".mkv" {
		return "video/x-mkv" // Samsung TVs do not accept the standard type
	}
	if mime := dlnaMime[ext]; mime != "" {
		return mime
	}
	return "application/octet-stream"
}

func dlnaDuration(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func mediaURL(base string, n *dlnaNode) string {
	// The file name is there only for players that show or sniff it.
	return base + "/media/" + n.ID + "/" + url.PathEscape(filepath.Base(n.Path))
}

// writeDIDL describes one folder or video in DIDL-Lite, the XML format of
// browse results.
func (s *DLNAServer) writeDIDL(b *strings.Builder, n *dlnaNode, base string, samsung bool) {
	art := ""
	if n.Art != "" {
		art = fmt.Sprintf(`<upnp:albumArtURI dlna:profileID="JPEG_TN">%s/art/%s</upnp:albumArtURI>`, base, n.ID)
	}
	if !n.IsItem() {
		fmt.Fprintf(b, `<container id="%s" parentID="%s" restricted="1" searchable="1" childCount="%d">`+
			`<dc:title>%s</dc:title><upnp:class>object.container.storageFolder</upnp:class>%s</container>`,
			n.ID, n.Parent, len(n.Children), xmlEsc(n.Title), art)
		return
	}
	fmt.Fprintf(b, `<item id="%s" parentID="%s" restricted="1"><dc:title>%s</dc:title><upnp:class>object.item.videoItem</upnp:class>`,
		n.ID, n.Parent, xmlEsc(n.Title))
	if n.Date != "" {
		fmt.Fprintf(b, "<dc:date>%s</dc:date>", xmlEsc(n.Date))
	}
	for _, g := range n.Genres {
		fmt.Fprintf(b, "<upnp:genre>%s</upnp:genre>", xmlEsc(g))
	}
	if n.Plot != "" {
		fmt.Fprintf(b, "<dc:description>%s</dc:description>", xmlEsc(n.Plot))
	}
	b.WriteString(art)

	attrs := fmt.Sprintf(`size="%d"`, n.Size)
	duration := time.Duration(n.Runtime) * time.Minute
	if info, ok := s.prober.get(n.Path, n.Size, n.ModTime); ok {
		if info.Duration > 0 {
			duration = info.Duration
		}
		if info.Width > 0 {
			attrs += fmt.Sprintf(` resolution="%dx%d"`, info.Width, info.Height)
		}
	}
	if duration > 0 {
		attrs += fmt.Sprintf(` duration="%s"`, dlnaDuration(duration))
	}
	fmt.Fprintf(b, `<res protocolInfo="http-get:*:%s:%s" %s>%s</res>`,
		mimeOf(n.Path, samsung), dlnaFeatures, attrs, xmlEsc(mediaURL(base, n)))
	if len(n.Subs) > 0 {
		sub := base + "/sub/" + n.ID
		fmt.Fprintf(b, `<res protocolInfo="http-get:*:text/srt:*">%s</res><sec:CaptionInfoEx sec:type="srt">%s</sec:CaptionInfoEx>`, sub, sub)
	}
	b.WriteString("</item>")
}

// item finds the video addressed by /<prefix>/<id>[/name].
func (s *DLNAServer) item(w http.ResponseWriter, r *http.Request, prefix string) *dlnaNode {
	lib, err := s.library()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, prefix), "/")
	n := lib.nodes[id]
	if n == nil {
		http.NotFound(w, r)
	}
	return n
}

// handleMedia streams a video. Only files of the scanned library can be
// reached: the URL holds an ID, never a path.
func (s *DLNAServer) handleMedia(w http.ResponseWriter, r *http.Request) {
	n := s.item(w, r, "/media/")
	if n == nil {
		return
	}
	if !n.IsItem() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(n.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	samsung := strings.Contains(r.UserAgent(), "SEC_HHP") || strings.Contains(r.UserAgent(), "Samsung")
	h := w.Header()
	h.Set("Content-Type", mimeOf(n.Path, samsung))
	h.Set("transferMode.dlna.org", "Streaming")
	h.Set("contentFeatures.dlna.org", dlnaFeatures)
	if len(n.Subs) > 0 && r.Header.Get("getCaptionInfo.sec") != "" {
		h.Set("CaptionInfo.sec", "http://"+r.Host+"/sub/"+n.ID)
	}
	// One line per playback, not per seek: players ask for the start of the
	// file first.
	if rng := r.Header.Get("Range"); r.Method == http.MethodGet && (rng == "" || strings.HasPrefix(rng, "bytes=0-")) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		s.log("▶ %s  %s", host, filepath.Base(n.Path))
	}
	http.ServeContent(w, r, "", n.ModTime, f) // handles Range and HEAD
}

func (s *DLNAServer) handleArt(w http.ResponseWriter, r *http.Request) {
	n := s.item(w, r, "/art/")
	if n == nil {
		return
	}
	if n.Art == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("transferMode.dlna.org", "Interactive")
	http.ServeFile(w, r, n.Art)
}

func (s *DLNAServer) handleSubtitles(w http.ResponseWriter, r *http.Request) {
	n := s.item(w, r, "/sub/")
	if n == nil {
		return
	}
	if len(n.Subs) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/srt; charset=utf-8")
	http.ServeFile(w, r, n.Subs[0])
}

// handleIndex is a plain page for a browser: what the server offers.
func (s *DLNAServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	lib, err := s.library()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1><p>DLNA media server: %d movie(s), %d episode(s).</p>",
		html.EscapeString(s.name), html.EscapeString(s.name), lib.movies, lib.episodes)
	var walk func(n *dlnaNode)
	walk = func(n *dlnaNode) {
		if n.IsItem() {
			fmt.Fprintf(&b, `<li><a href="%s">%s</a></li>`, html.EscapeString(mediaURL("", n)), html.EscapeString(n.Title))
			return
		}
		fmt.Fprintf(&b, "<li>%s<ul>", html.EscapeString(n.Title))
		for _, c := range n.Children {
			walk(c)
		}
		b.WriteString("</ul></li>")
	}
	b.WriteString("<ul>")
	walk(lib.nodes[dlnaMoviesID])
	walk(lib.nodes[dlnaSeriesID])
	b.WriteString("</ul>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, b.String())
}
