package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	_ "github.com/lib/pq"
)

const (
	InQueue    uint = 0
	Processing uint = 1
	Complete   uint = 2
	Error      uint = 3
)

var ditchNetConfig struct {
	DatabaseHost          string `json:"database_host"`
	DatabasePort          uint   `json:"database_port"`
	DatabaseUsername      string `json:"database_username"`
	DatabasePassword      string `json:"database_password"`
	DatabaseName          string `json:"database_name"`
	InputFileStoragePath  string `json:"input_file_storage_path"`
	OutputFileStoragePath string `json:"output_file_storage_path"`
	ListenClient          string `json:"listen_client"`
	ListenPort            uint   `json:"listen_port"`
	ScriptPath            string `json:"script_path"`
	AssetsPath            string `json:"assets_path"`
	MaxConcurrentJobs     uint   `json:"max_concurrent_jobs"`
}

type ditchNetState struct {
	State   uint
	Message string
}

func (dns ditchNetState) toJSON() []byte {
	return []byte(fmt.Sprintf(`{"state_id": %d, "message": "%s"}`, dns.State, dns.Message))
}

type ditchNetJob string

func (dnj ditchNetJob) getState(db *sql.DB) (*uint, error) {
	row := db.QueryRow("SELECT state FROM jobs WHERE job_id = $1", dnj)
	var state uint
	if err := row.Scan(&state); err != nil {
		return nil, err
	}

	return &state, nil
}

func (dnj ditchNetJob) getPositionInQueue(db *sql.DB) (*uint, error) {
	row := db.QueryRow("SELECT COUNT(*) FROM jobs WHERE state = $1 AND added < (SELECT added FROM jobs WHERE job_id = $2)", InQueue, dnj)
	var pos uint
	if err := row.Scan(&pos); err != nil {
		return nil, err
	}

	return &pos, nil
}

func (dnj ditchNetJob) getInFilePath() string {
	return path.Join(ditchNetConfig.InputFileStoragePath, fmt.Sprintf("%s.tiff", dnj))
}

func (dnj ditchNetJob) getOutFilePath() string {
	return path.Join(ditchNetConfig.OutputFileStoragePath, fmt.Sprintf("%s_processed.tiff", dnj))
}

func (dnj ditchNetJob) setState(db *sql.DB, state uint) {
	db.Exec("UPDATE jobs SET state = $1, changed = NOW() WHERE job_id = $2", state, dnj)
}

func (dnj ditchNetJob) start() {
	db := getDBConnection()
	defer db.Close()

	log.Printf("starting job %s\n", dnj)
	dnj.setState(db, Processing)

	inFile := dnj.getInFilePath()
	outFile := dnj.getOutFilePath()

	cmd := exec.Command("/usr/bin/python3", ditchNetConfig.ScriptPath, inFile, outFile)
	err := cmd.Run()
	if err != nil {
		dnj.setState(db, Error)
		log.Printf("job '%s' failed: '%v'\n", dnj, err)
		return
	}

	dnj.setState(db, Complete)
}

func getProcessingCount(db *sql.DB) uint {
	row := db.QueryRow("SELECT COUNT(*) FROM jobs WHERE state = $1", Processing)
	var pCount uint
	if err := row.Scan(&pCount); err != nil {
		log.Printf("while getting count of currently running processes: '%v'\n", err)
		return ditchNetConfig.MaxConcurrentJobs + 1
	}

	return pCount
}

func getNextJob(db *sql.DB) *ditchNetJob {
	row := db.QueryRow("SELECT job_id FROM jobs WHERE state = $1 ORDER BY added LIMIT 1", InQueue)
	var job ditchNetJob
	if err := row.Scan(&job); err != nil {
		return nil
	}

	return &job
}

func jobQueueRoutine() {
	db := getDBConnection()
	defer db.Close()

	for true {
		time.Sleep(1 * time.Second)

		if getProcessingCount(db) < ditchNetConfig.MaxConcurrentJobs {
			job := getNextJob(db)
			if job == nil {
				continue
			}

			go job.start()
		}
	}
}

func cleanerRoutine() {
	db := getDBConnection()
	defer db.Close()

	for true {
		time.Sleep(2 * time.Hour)

		rows, err := db.Query("SELECT job_id FROM jobs WHERE state IN ($1,$2) AND NOW() > (changed + '2 hours'::interval)")
		if err != nil {
			log.Printf("error while listing old jobs: '%v'\n", err)
			continue
		}

		var job ditchNetJob
		for rows.Next() {
			if err := rows.Scan(&job); err != nil {
				log.Printf("error while getting job_id from database: '%v'\n", err)
				continue
			}

			log.Printf("purging job %s", job)
			err := os.Remove(job.getInFilePath())
			if err != nil && !os.IsNotExist(err) {
				log.Printf("unable to delete input-file: '%v'\n", err)
			}

			err = os.Remove(job.getOutFilePath())
			if err != nil && !os.IsNotExist(err) {
				log.Printf("unable to delete output-file: '%v'\n", err)
			}

			_, err = db.Exec("DELETE FROM jobs WHERE job_id = $1", job)
			if err != nil {
				log.Printf("unable to delete job '%s' from database: '%v'\n", job, err)
			}
		}
	}
}

func getDBConnection() *sql.DB {
	DB, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable", ditchNetConfig.DatabaseHost, ditchNetConfig.DatabasePort, ditchNetConfig.DatabaseUsername, ditchNetConfig.DatabasePassword, ditchNetConfig.DatabaseName))

	if err != nil {
		log.Panic(err)
	}
	if err = DB.Ping(); err != nil {
		log.Panic(err)
	}

	return DB
}

func newJobHandler(w http.ResponseWriter, r *http.Request) {
	db := getDBConnection()
	defer db.Close()

	job := ditchNetJob(uuid.New().String())
	reader, err := r.MultipartReader()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "expected multipart data")
		log.Printf("could not read multipart form data: '%v'\n", err)
		return
	}

	frm, err := reader.ReadForm(10000000)
	if err != nil {
		log.Panic(err)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "unable to read submitted data")
		log.Printf("error while reading multipart form data: '%v'\n", err)
		return
	}

	model := frm.Value["model"][0]
	if model != "1" && model != "2" {
		w.WriteHeader(http.StatusFailedDependency)
		fmt.Fprint(w, "no model provided, expected '1' for 0.5m² or '2' for 1m² pixel resolution")
		return
	}

	var fh []*multipart.FileHeader
	var ok bool
	fh, ok = frm.File["input"]
	if !ok {
		w.WriteHeader(http.StatusFailedDependency)
		fmt.Fprint(w, "no input-file provided")
		return
	}

	f := fh[0]
	file, err := f.Open()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	ct := f.Header.Get("Content-Type")
	if ct != "image/tiff" {
		w.WriteHeader(http.StatusNotAcceptable)
		fmt.Fprint(w, "wrong content-type, expected 'image/tiff'")
		log.Println("recieved file with wrong content-type (not 'image/tiff')")
		return
	}

	/*if err != nil {
		w.WriteHeader(http.StatusNotAcceptable)
		fmt.Fprint(w, "unable to decode your image file")
		log.Printf("unable to decode image: '%v'\n", err)
		return
	}

	if img.Bounds().Max.X > 1000 || img.Bounds().Max.Y > 1000 {
		w.WriteHeader(http.StatusNotAcceptable)
		fmt.Fprint(w, "your image is too large, maximum dimensions are 1000x1000 pixels.")
		log.Println("image to large")
		return
	}*/

	filename := job.getInFilePath()
	nf, err := os.Create(filename)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Panic(err)
	}

	_, err = io.Copy(nf, file)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Panic(err)
	}
	file.Close()
	nf.Close()

	_, err = db.Exec("INSERT INTO jobs(job_id, model, original_filename) VALUES($1, $2, $3)", job, model, f.Filename)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Panic(err)
	}

	fmt.Fprint(w, job)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	f, err := os.Open(path.Join(ditchNetConfig.AssetsPath, "index.html"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		log.Println(err)
		return
	}

	io.Copy(w, f)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Missing configuration file path, run as 'ditchnet /path/to/configuration_file.json'")
		os.Exit(1)
	}

	log.Println("reading configuration")
	confFile, err := os.Open(os.Args[1])
	if err != nil {
		log.Panic(err)
	}
	defer confFile.Close()

	rawConfig, err := ioutil.ReadAll(confFile)
	if err != nil {
		log.Panic(err)
	}

	err = json.Unmarshal(rawConfig, &ditchNetConfig)
	if err != nil {
		log.Panic(err)
	}

	r := mux.NewRouter()
	r.HandleFunc("/", indexHandler).Methods("GET")
	r.HandleFunc("/job", newJobHandler).Methods("POST")
	r.PathPrefix("/assets").Handler(http.StripPrefix("/assets", http.FileServer(http.Dir(path.Join(ditchNetConfig.AssetsPath, "assets")))))

	go jobQueueRoutine()
	log.Printf("started job queue")

	go cleanerRoutine()
	log.Printf("started periodic file-cleaner")

	log.Printf("starting webserver listening for connections on '%s:%d'\n", ditchNetConfig.ListenClient, ditchNetConfig.ListenPort)
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", ditchNetConfig.ListenClient, ditchNetConfig.ListenPort), r); err != nil {
		log.Panic(err)
	}
}
