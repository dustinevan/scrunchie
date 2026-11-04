// Package tui implements scrunch's terminal editor and result browser.
package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/lib/logic/jsontree"
	"github.com/dustinevan/scrunchie/pkg/result"
)

// Config supplies the startup connection and explicit startup SQL. New adopts
// Session as the workspace's first open connection.
type Config struct {
	Session            *postgres.Session
	Connection         string
	InitialSQL         string
	InitialPath        string
	Limits             result.Limits
	Timeout            time.Duration
	Demo               bool
	DSN                string
	ConnectTimeout     time.Duration
	ConnectionExplicit bool
	ConnectionError    string
	Profiles           []connections.Profile
	ProfilesPath       string
	ProfilesError      string
	// AllowWrites is the write mode for Session and for reconnecting to DSN.
	// Saved profiles carry their own mode.
	AllowWrites bool
}

type pane uint8

const (
	editorPane pane = iota
	resultsPane
	catalogPane
)

// buffer is one SQL editor tab. conn names the open connection its SQL runs
// on, or 0 when it has none; used orders buffers by when they were last
// current, so activating a connection returns to its latest buffer.
type buffer struct {
	id              uint64
	path            string
	saved           string
	editor          editor
	dirty           bool
	checkedRevision uint64
	conn            uint64
	used            uint64
	results         *bufferResults
}

// bufferResults is the output of a buffer's last run and how the grid shows
// it. Each buffer keeps its own, so switching buffers shows that buffer's
// results; closing the buffer frees them.
type bufferResults struct {
	batch result.Batch
	// sql produced batch; copied INSERTs name its table. origin re-runs it
	// for fetch more. err replaces results after a run that returned none.
	sql    string
	origin runOrigin
	err    string
	// conn and connName identify the connection that produced batch, which
	// may since have closed. serial is that run's request; a newer batch
	// ends a grid drag that began on an older one.
	conn     uint64
	connName string
	serial   uint64
	// status, statusError, and message repeat the run's status line when the
	// buffer becomes current again.
	status      string
	statusError bool
	message     string

	setIndex     int
	row          int
	column       int
	gridTop      int
	gridLeft     int
	formats      []map[int]cell.Format
	rowAnchor    int
	columnAnchor int
	widths       [][]int
	// view orders and filters the current result set.
	view rowView
}

func newResults() *bufferResults {
	return &bufferResults{rowAnchor: -1, columnAnchor: -1}
}

// connection is one open PostgreSQL session and the state that belongs to
// it: write mode, transaction state, the query it runs, and its catalog. Each
// connection serves one request at a time; different connections run
// concurrently. Its id is never reused, so replies for a closed connection
// find nothing and are dropped.
type connection struct {
	id          uint64
	name        string // short label for tabs, rows, history, and notices
	description string // user@host:port/database, without secrets
	// profile holds the settings a saved or form connection opened with; its
	// ID matches the saved entry. Command-line connections have no profile
	// and reconnect to dsn. password is a value typed at Connect, kept in
	// memory only so Reconnect can authenticate again.
	profile     connections.Profile
	password    string
	dsn         string
	allowWrites bool
	session     *postgres.Session
	connected   bool
	txStatus    byte
	writable    bool // the session's write mode at the last event-loop update

	query queryState

	catalog        []catalogNode
	collapsed      bool
	catalogBusy    bool
	catalogCancel  context.CancelFunc
	catalogSerial  uint64
	catalogQueue   []string
	refreshPending bool
	completion     completionCatalog
}

// queryState is the one SQL or write-mode request a connection is serving.
// queued means it waits for a catalog request on the same connection.
type queryState struct {
	running     bool
	canceling   bool
	cancel      context.CancelFunc
	serial      uint64
	started     time.Time
	sql         string
	buffer      uint64
	focus       pane
	interaction uint64
	queued      bool
	offset      int
	source      string
	line        int
	// page is the row page requested for a plain read; fetchMore records
	// that this run re-ran a result, so its reply keeps the grid selection.
	page      int
	fetchMore bool
}

type historyEntry struct {
	sql      string
	at       time.Time
	duration time.Duration
	rows     int64
	err      string
	more     bool
	conn     uint64
	connName string
}

type completion struct {
	items    []string // replacement text
	details  []string // kind and type hints, one per item
	index    int
	start    int
	end      int
	buffer   uint64 // buffer and editor revision the range belongs to
	revision uint64
	auto     bool // opened by typing rather than an explicit request
}

type modal struct {
	kind          string
	title         string
	input         editor
	index         int
	message       string
	buttons       []string
	error         string
	historyRows   []int
	historyReady  bool
	historyQuery  string
	historySerial uint64
	// target is the connection a confirmation acts on, or the buffer the
	// connection chooser rebinds; choices are the connection ids it lists,
	// with 0 for none.
	target  uint64
	choices []uint64
}

type inspector struct {
	active    bool
	value     result.Cell
	column    result.Column
	format    cell.Format
	tree      jsontree.Tree
	json      bool
	index     int
	top       int
	left      int
	filter    string
	error     string
	lines     []string
	visible   []int
	scrolling bool
	// row is the grid position; source is the retained row it maps to.
	row    int
	source int
}

type fileJob struct {
	kind     string
	path     string
	sql      string
	bufferID uint64
	set      result.Set
	format   string
	modal    bool
	// scope names exported rows for the status, such as "filtered view".
	scope string
	// conn binds a file opened into a new buffer.
	conn uint64
}

// Model owns all terminal state. Database and file commands return messages;
// they do not mutate the model from a background goroutine.
type Model struct {
	config          Config
	width           int
	height          int
	focus           pane
	interaction     uint64
	mouse           mouseState
	modalMouseState modalMouseState

	buffers              []buffer
	bufferIndex          int
	nextBuffer           uint64
	useClock             uint64
	completion           completion
	completeAfterCatalog bool

	// connections are open sessions in the order they were opened. serial
	// numbers every database request, so a reply can be matched to the
	// request its connection is waiting for. closing holds sessions being
	// closed in the background so shutdown can finish them.
	connections    []*connection
	nextConnection uint64
	serial         uint64
	closing        []*postgres.Session

	gridScrolling bool
	copyAsIndex   int

	catalogFilter string
	databasePane  databasePaneState
	// closedWithQ is true right after q closed a view, so a repeated q is
	// not typed into the SQL or the database filter underneath.
	closedWithQ bool

	pendingRun    queryRun
	history       []historyEntry
	historySerial uint64
	spinner       spinner.Model

	// fetchPending asks the next runSQL to re-run the current results' origin.
	fetchPending bool

	modal             modal
	modalSerial       uint64
	inspector         inspector
	structure         structureView
	ddl               ddlRequest
	fileJob           fileJob
	fileBusy          bool
	quitIntent        bool
	closeIntent       bool
	confirmBuffer     uint64
	quitDiscard       map[uint64]bool
	quitConfirmed     bool
	connecting        bool
	connectCancel     context.CancelFunc
	reconnectAttempt  *connectionAttempt
	profiles          []connections.Profile
	profileIndex      int
	profileBusy       bool
	connectionForm    connectionForm
	discovering       bool
	discoveryCancel   context.CancelFunc
	discoverySerial   uint64
	discoveryAttempt  *connectionAttempt
	databaseNames     []string
	databaseNameIndex int

	showCatalog     bool
	maximized       bool
	status          string
	statusError     bool
	lastMessage     string
	lastSearch      string
	searchPane      pane
	searchInspector bool
}

// New initializes an editor without persisting session state. A configured
// session becomes the first open connection, bound to the first buffer.
func New(config Config) *Model {
	m := &Model{
		config: config, width: 100, height: 30,
		showCatalog: true, nextBuffer: 1, quitDiscard: make(map[uint64]bool),
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		status:  "Ctrl+J run · Ctrl+P commands · F1 help",
	}
	m.buffers = []buffer{{id: 1, path: config.InitialPath, editor: newEditor(config.InitialSQL), results: newResults()}}
	m.buffers[0].saved = string(m.buffers[0].editor.text)
	m.profiles = append([]connections.Profile(nil), config.Profiles...)
	if config.Session != nil && !config.Demo {
		m.nextConnection++
		c := &connection{id: m.nextConnection, description: config.Connection, dsn: config.DSN, allowWrites: config.AllowWrites,
			session: config.Session, connected: true, txStatus: 'I', writable: !config.Session.ReadOnly()}
		if c.description == "" {
			c.description = config.Session.Description()
		}
		c.name = connectionLabel(connections.Profile{}, config.Session, c.description)
		m.connections = append(m.connections, c)
		m.buffers[0].conn = c.id
		// The connection owns the session from here; Close closes it.
		m.config.Session = nil
	} else if !config.Demo {
		m.status = "Ctrl+B add a database · Ctrl+P commands · F1 help"
	}
	if config.ConnectionError != "" {
		m.status, m.statusError = config.ConnectionError, true
		m.lastMessage = config.ConnectionError
	} else if config.ProfilesError != "" {
		m.status, m.statusError = "Saved databases: "+config.ProfilesError+" · Ctrl+B to connect without saving", true
	}
	if config.Demo {
		m.initDemo()
	}
	m.useClock++
	m.buffers[0].used = m.useClock
	m.prepareView()
	return m
}

// Init starts a catalog request only when a PostgreSQL session exists.
func (m *Model) Init() tea.Cmd {
	if m.config.Demo || len(m.connections) == 0 {
		return nil
	}
	return m.loadCatalog(m.connections[0], -1)
}

// Cancel stops outstanding work before the caller closes the sessions.
func (m *Model) Cancel() {
	for _, c := range m.connections {
		if c.query.cancel != nil {
			c.query.cancel()
		}
		if c.catalogCancel != nil {
			c.catalogCancel()
		}
	}
	if m.connectCancel != nil {
		m.connectCancel()
	}
	if m.discoveryCancel != nil {
		m.discoveryCancel()
	}
}

type queryMsg struct {
	conn   uint64
	serial uint64
	batch  result.Batch
	err    error
}

// queryRun is SQL chosen for one Run request. A confirmation keeps it so the
// SQL that was checked is exactly the SQL that is sent, on the same session.
type queryRun struct {
	sql     string
	offset  int // rune offset of sql in source
	line    int // one-based buffer line of the first SQL token
	buffer  uint64
	source  string
	conn    uint64
	session *postgres.Session
	page    int  // rows requested for a plain read
	more    bool // fetch more: re-run the current result with a larger page
}

// catalogMsg answers one catalog request: tree nodes when kind is empty,
// otherwise a "structure" snapshot or "ddl" text.
type catalogMsg struct {
	conn      uint64
	serial    uint64
	kind      string
	parent    int
	nodes     []catalogNode
	structure postgres.Structure
	text      string
	err       error
}

type fileMsg struct {
	job    fileJob
	sql    string
	exists bool
	check  bool
	err    error
}
