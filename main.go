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
	NotFound   uint = 4
)

var ditchNetConfig struct {
	DatabaseHost      string        `json:"database_host"`
	DatabasePort      uint          `json:"database_port"`
	DatabaseUsername  string        `json:"database_username"`
	DatabasePassword  string        `json:"database_password"`
	DatabaseName      string        `json:"database_name"`
	FileStoragePath   string        `json:"file_storage_path"`
	ListenClient      string        `json:"listen_client"`
	ListenPort        uint          `json:"listen_port"`
	JobTimeoutMin     time.Duration `json:"job_timeout_min"`
	AssetsPath        string        `json:"assets_path"`
	MaxConcurrentJobs uint          `json:"max_concurrent_jobs"`
}

type ditchNetJob string

func (dnj ditchNetJob) getState(db *sql.DB) uint {
	row := db.QueryRow("SELECT state FROM jobs WHERE job_id = $1", dnj)
	var state uint
	if err := row.Scan(&state); err != nil {
		return NotFound
	}

	return state
}

func (dnj ditchNetJob) getPositionInQueue(db *sql.DB) (*uint, error) {
	row := db.QueryRow("SELECT COUNT(*) FROM jobs WHERE state = $1 AND added < (SELECT added FROM jobs WHERE job_id = $2)", InQueue, dnj)
	var pos uint
	if err := row.Scan(&pos); err != nil {
		return nil, err
	}

	return &pos, nil
}

func (dnj ditchNetJob) getFolder() string {
	return path.Join(ditchNetConfig.FileStoragePath, string(dnj))
}

func (dnj ditchNetJob) getInFolderPath() string {
	return path.Join(dnj.getFolder(), "input")
}

func (dnj ditchNetJob) getInFilePath() string {
	return path.Join(dnj.getInFolderPath(), "target.tif")
}

func (dnj ditchNetJob) getOutFolderPath() string {
	return path.Join(dnj.getFolder(), "output")
}

func (dnj ditchNetJob) getOutFilePath() string {
	return path.Join(dnj.getOutFolderPath(), "target.tif")
}

func (dnj ditchNetJob) getTempFolderPath() string {
	return path.Join(dnj.getFolder(), "temp")
}

func (dnj ditchNetJob) getOriginalFilename(db *sql.DB) string {
	row := db.QueryRow("SELECT original_filename FROM jobs WHERE job_id = $1", dnj)
	var fn string
	if err := row.Scan(&fn); err != nil {
		log.Printf("could not get original filename for job %s: '%v'\n", dnj, err)
		return "target.tif"
	}

	return fn
}

func (dnj ditchNetJob) getStateAndMessage(db *sql.DB) (uint, string) {
	state := dnj.getState(db)

	var msg string

	if state == InQueue {
		pos, err := dnj.getPositionInQueue(db)
		if err != nil {
			log.Printf("could not position in queue for job %s: '%v'\n", dnj, err)
			return InQueue, "position in queue is unknown"
		}

		msg = fmt.Sprintf("position in queue: %d", *pos)
	} else if state == Processing {
		msg = "processing"
	} else if state == Complete {
		msg = "complete"
	} else if state == Error {
		msg = "failed"
	} else {
		msg = "unknown state"
	}

	return state, msg
}

func (dnj ditchNetJob) setState(db *sql.DB, state uint) {
	db.Exec("UPDATE jobs SET state = $1, changed = NOW() WHERE job_id = $2", state, dnj)
}

func (dnj ditchNetJob) getModel(db *sql.DB) uint {
	row := db.QueryRow("SELECT model FROM jobs WHERE job_id = $1", dnj)
	var model uint
	if err := row.Scan(&model); err != nil {
		log.Printf("could not get model for job %s from db: '%v'\n", dnj, err)
		return 1
	}

	return model
}

func (dnj ditchNetJob) start() {
	db := getDBConnection()
	defer db.Close()

	log.Printf("starting job %s\n", dnj)
	dnj.setState(db, Processing)

	var modelPath string
	if dnj.getModel(db) == 1 {
		modelPath = "/min/modell/DitchNet_05m.h5"
	} else {
		modelPath = "/min/modell/DitchNet_1m.h5"
	}

	cmd := exec.Command(
		"docker",
		"run",
		"--rm",
		"-t",
		"--gpus=all",
		"-v", fmt.Sprintf("%s:/min/input", dnj.getInFolderPath()),
		"-v", fmt.Sprintf("%s:/min/output", dnj.getOutFolderPath()),
		"-v", fmt.Sprintf("%s:/min/temp_dir", dnj.getTempFolderPath()),
		"ditchnet",
		"python", "/min/modell/script.py",
		"/min/input/",
		"/min/output/",
		"--temp_dir=/min/temp_dir/",
		fmt.Sprintf("--model=%s", modelPath),
	)
	running := true
	go func() {
		time.Sleep(ditchNetConfig.JobTimeoutMin * time.Minute)
		if running {
			log.Printf("job %s is taking too long\n", dnj)
			err := cmd.Process.Kill()
			if err != nil {
				log.Printf("failed to kill job %s: '%v'\n", dnj, err)
			}
		}
	}()
	out, err := cmd.CombinedOutput()
	running = false
	if err != nil {
		log.Printf("job %s closed with error: '%v'\n", dnj, err)
	}

	log.Printf("job %s output: '%s", dnj, string(out))

	_, err = os.Stat(dnj.getOutFilePath())
	if err != nil {
		log.Printf("job %s failed, unable to stat outfile: '%v'\n", dnj, err)
		dnj.setState(db, Error)
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
		time.Sleep(5 * time.Second)

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

			err = os.RemoveAll(job.getFolder())
			if err != nil && !os.IsNotExist(err) {
				log.Printf("unable to delete job files: '%v'\n", err)
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
		fmt.Fprint(w, "no model provided, expected '1' for 0.5m?? or '2' for 1m?? pixel resolution")
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

	err = os.MkdirAll(job.getInFolderPath(), 0755)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Panic(err)
	}

	err = os.MkdirAll(job.getOutFolderPath(), 0755)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Panic(err)
	}

	err = os.MkdirAll(job.getTempFolderPath(), 0755)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Panic(err)
	}

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

func getJobStateHandler(w http.ResponseWriter, r *http.Request) {
	db := getDBConnection()
	defer db.Close()

	var job ditchNetJob = ditchNetJob(mux.Vars(r)["job"])
	state, msg := job.getStateAndMessage(db)

	fmt.Fprintf(w, `{"state_id": %d, "message": "%s"}`, state, msg)
}

func getJobOutput(w http.ResponseWriter, r *http.Request) {
	db := getDBConnection()
	defer db.Close()

	job := ditchNetJob(mux.Vars(r)["job"])

	f, err := os.Open(job.getOutFilePath())

	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			log.Printf("could not read job %s output file: '%v'\n", job, err)
			return
		}
	}

	w.Header().Set("Content-disposition", fmt.Sprintf("attachment; filename=\"%s\"", job.getOriginalFilename(db)))
	_, err = io.Copy(w, f)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Printf("could not send file to client: '%v'\n", err)
	}
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
	r.HandleFunc("/job/{job:[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}}", getJobStateHandler).Methods("GET")
	r.HandleFunc("/job/{job:[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}}/download", getJobOutput).Methods("GET")
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
