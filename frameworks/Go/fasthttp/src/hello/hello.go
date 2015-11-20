package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"html/template"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"

	_ "github.com/go-sql-driver/mysql"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/reuseport"
)

type Message struct {
	Message string `json:"message"`
}

type World struct {
	Id           uint16 `json:"id"`
	RandomNumber uint16 `json:"randomNumber"`
}

type Fortune struct {
	Id      uint16 `json:"id"`
	Message string `json:"message"`
}

// Databases
const (
	connectionString   = "benchmarkdbuser:benchmarkdbpass@tcp(localhost:3306)/hello_world?interpolateParams=true"
	worldRowCount      = 10000
	maxConnectionCount = 256
)

var (
	worldSelectStmt   *sql.Stmt
	worldUpdateStmt   *sql.Stmt
	fortuneSelectStmt *sql.Stmt
)

const helloWorldString = "Hello, World!"

var (
	// Templates
	tmpl = template.Must(template.ParseFiles("templates/layout.html", "templates/fortune.html"))

	// Database
	db *sql.DB

	helloWorldBytes = []byte(helloWorldString)
)

var (
	listenAddr = flag.String("listenAddr", ":8080", "Address to listen to")
	prefork    = flag.Bool("prefork", false, "use prefork")
	child      = flag.Bool("child", false, "is child proc")
)

func main() {
	flag.Parse()

	var listener net.Listener
	if !*prefork {
		runtime.GOMAXPROCS(runtime.NumCPU())
	} else {
		listener = doPrefork()
	}

	var err error
	db, err = sql.Open("mysql", connectionString)
	if err != nil {
		log.Fatalf("Error opening database: %s", err)
	}
	if err = db.Ping(); err != nil {
		log.Fatalf("Cannot connect to db: %s", err)
	}

	dbConnCount := maxConnectionCount
	if *prefork {
		dbConnCount = (dbConnCount + runtime.NumCPU()-1) / runtime.NumCPU()
	}
	db.SetMaxIdleConns(dbConnCount)
	db.SetMaxOpenConns(dbConnCount * 2)

	worldSelectStmt = mustPrepare(db, "SELECT id, randomNumber FROM World WHERE id = ?")
	worldUpdateStmt = mustPrepare(db, "UPDATE World SET randomNumber = ? WHERE id = ?")
	fortuneSelectStmt = mustPrepare(db, "SELECT id, message FROM Fortune")

	s := &fasthttp.Server{
		Handler: mainHandler,
		Name:    "fasthttp",
	}
	if !*prefork {
		s.ListenAndServe(*listenAddr)
	} else {
		s.Serve(listener)
	}
}

func mustPrepare(db *sql.DB, query string) *sql.Stmt {
	stmt, err := db.Prepare(query)
	if err != nil {
		log.Fatalf("Error when preparing statement %q: %s", query, err)
	}
	return stmt
}

func doPrefork() net.Listener {
	if !*child {
		children := make([]*exec.Cmd, runtime.NumCPU())
		for i := range children {
			children[i] = exec.Command(os.Args[0], "-prefork", "-child")
			children[i].Stdout = os.Stdout
			children[i].Stderr = os.Stderr
			if err := children[i].Start(); err != nil {
				log.Fatal(err)
			}
		}
		for _, ch := range children {
			if err := ch.Wait(); err != nil {
				log.Print(err)
			}
		}
		os.Exit(0)
		return nil
	}

	runtime.GOMAXPROCS(1)
	listener, err := reuseport.Listen("tcp4", *listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	return listener
}

func mainHandler(ctx *fasthttp.RequestCtx) {
	path := ctx.Path()
	switch {
	case fasthttp.EqualBytesStr(path, "/plaintext"):
		plaintextHandler(ctx)
	case fasthttp.EqualBytesStr(path, "/json"):
		jsonHandler(ctx)
	case fasthttp.EqualBytesStr(path, "/db"):
		dbHandler(ctx)
	case fasthttp.EqualBytesStr(path, "/queries"):
		queriesHandler(ctx)
	case fasthttp.EqualBytesStr(path, "/fortune"):
		fortuneHandler(ctx)
	case fasthttp.EqualBytesStr(path, "/update"):
		updateHandler(ctx)
	default:
		ctx.Error("unexpected path", fasthttp.StatusBadRequest)
	}
}

// Test 1: JSON serialization
func jsonHandler(ctx *fasthttp.RequestCtx) {
	jsonMarshal(ctx, &Message{helloWorldString})
}

// Test 2: Single database query
func dbHandler(ctx *fasthttp.RequestCtx) {
	var w World
	fetchRandomWorld(&w)
	jsonMarshal(ctx, &w)
}

// Test 3: Multiple database queries
func queriesHandler(ctx *fasthttp.RequestCtx) {
	n := getQueriesCount(ctx)

	worlds := make([]World, n)
	for i := 0; i < n; i++ {
		fetchRandomWorld(&worlds[i])
	}

	jsonMarshal(ctx, worlds)
}

// Test 4: Fortunes
func fortuneHandler(ctx *fasthttp.RequestCtx) {
	rows, err := fortuneSelectStmt.Query()
	if err != nil {
		log.Fatalf("Error preparing statement: %v", err)
	}

	fortunes := make(Fortunes, 0, 16)
	for rows.Next() {
		var f Fortune
		if err := rows.Scan(&f.Id, &f.Message); err != nil {
			log.Fatalf("Error scanning fortune row: %s", err)
		}
		fortunes = append(fortunes, &f)
	}
	rows.Close()
	fortunes = append(fortunes, &Fortune{Message: "Additional fortune added at request time."})

	sort.Sort(ByMessage{fortunes})

	ctx.SetContentType("text/html")
	if err := tmpl.Execute(ctx, fortunes); err != nil {
		log.Fatalf("Error executing fortune: %s", err)
	}
}

// Test 5: Database updates
func updateHandler(ctx *fasthttp.RequestCtx) {
	n := getQueriesCount(ctx)

	worlds := make([]World, n)
	for i := 0; i < n; i++ {
		w := &worlds[i]
		fetchRandomWorld(w)
		w.RandomNumber = uint16(randomWorldNum())
		if _, err := worldUpdateStmt.Exec(w.RandomNumber, w.Id); err != nil {
			log.Fatalf("Error updating world row: %s", err)
		}
	}

	jsonMarshal(ctx, worlds)
}

// Test 6: Plaintext
func plaintextHandler(ctx *fasthttp.RequestCtx) {
	ctx.Success("text/plain", helloWorldBytes)
}

func jsonMarshal(ctx *fasthttp.RequestCtx, v interface{}) {
	ctx.SetContentType("application/json")
	if err := json.NewEncoder(ctx).Encode(v); err != nil {
		log.Fatalf("error in json.Encoder.Encode: %s", err)
	}
}

func fetchRandomWorld(w *World) {
	n := randomWorldNum()
	if err := worldSelectStmt.QueryRow(n).Scan(&w.Id, &w.RandomNumber); err != nil {
		log.Fatalf("Error scanning world row: %s", err)
	}
}

func randomWorldNum() int {
	return rand.Intn(worldRowCount) + 1
}

func getQueriesCount(ctx *fasthttp.RequestCtx) int {
	n := ctx.QueryArgs().GetUintOrZero("queries")
	if n < 1 {
		n = 1
	} else if n > 500 {
		n = 500
	}
	return n
}

type Fortunes []*Fortune

func (s Fortunes) Len() int      { return len(s) }
func (s Fortunes) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

type ByMessage struct{ Fortunes }

func (s ByMessage) Less(i, j int) bool { return s.Fortunes[i].Message < s.Fortunes[j].Message }
