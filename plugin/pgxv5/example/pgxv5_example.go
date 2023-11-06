package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5"
)

func getDBPool() *pgxpool.Pool {
	ctx := context.Background()

	urlExample := "postgresql://testuser:p123@localhost/testdb?sslmode=disable"
	config, err := pgxpool.ParseConfig(urlExample)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to parse connection config: %v\n", err)
		os.Exit(1)
	}

	config.ConnConfig.Tracer = pppgxv5.NewPgxTracer()

	dbpool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to create connection pool: %v\n", err)
		os.Exit(1)
	}

	rows, err := dbpool.Query(ctx, "select 1")
	if err != nil {
		panic(err)
	}

	if err := rows.Err(); err != nil {
		panic(err)
	}

	log.Println("successfully connected to db")

	return dbpool
}

func tableCount(w http.ResponseWriter, r *http.Request) {
	dbPool := getDBPool()

	if dbPool == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "db connection fail")
		return
	}
	//defer dbPool.Close()

	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)

	rows := dbPool.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")

	var count int
	err := rows.Scan(&count)
	if err != nil {
		log.Fatalf("sql error: %v", err)
	}

	fmt.Println("number of entries in pg_catalog.pg_tables", count)
	io.WriteString(w, "success")
}

func query(w http.ResponseWriter, r *http.Request) {
	dbPool := getDBPool()

	if dbPool == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "db connection fail")
		return
	}
	//defer dbPool.Close()

	ctx := pinpoint.NewContext(context.Background(), pinpoint.TracerFromRequestContext(r))

	_, _ = dbPool.Exec(ctx, "CREATE TABLE employee (id INTEGER PRIMARY KEY, emp_name VARCHAR(64), department VARCHAR(64), created DATE)")

	insert := "INSERT INTO employee VALUES ($1, $2, $3, $4)"

	_, _ = dbPool.Exec(ctx, insert, 1, "foo", "pinpoint", "2022-08-15")
	_, _ = dbPool.Exec(ctx, insert, 2, "bar", "avengers", "2022-08-16")

	update := "UPDATE employee SET emp_name = $1 where id = $2"

	res, _ := dbPool.Exec(ctx, update, "ironman", 2)
	_ = res.RowsAffected()

	var (
		uid        int
		empName    string
		department string
		created    string
	)

	rows, _ := dbPool.Query(ctx, "SELECT * FROM employee WHERE id = 1")
	for rows.Next() {
		_ = rows.Scan(&uid, &empName, &department, &created)
		fmt.Printf("user: %d, %s, %s, %s\n", uid, empName, department, created)
	}
	rows.Close()

	rows, _ = dbPool.Query(ctx, "SELECT * FROM employee WHERE id = $1", 1)
	for rows.Next() {
		_ = rows.Scan(&uid, &empName, &department, &created)
		fmt.Printf("user: %d, %s, %s, %s\n", uid, empName, department, created)
	}
	rows.Close()

	tx(ctx, dbPool)

	_, _ = dbPool.Exec(ctx, "DROP TABLE employee")
}

func tx(ctx context.Context, db *pgxpool.Pool) {
	tx, err := db.Begin(ctx)
	if err != nil {
		log.Fatal(err)
	}

	_, err = tx.Exec(ctx, "INSERT INTO employee VALUES (3, 'ipad', 'apple', '2022-08-15'), ($1, $2, $3, $4)",
		4, "chrome", "google", "2022-08-18")
	if err != nil {
		tx.Rollback(ctx)
		return
	}

	row := tx.QueryRow(ctx, "SELECT count(*) FROM employee")
	var count int
	err = row.Scan(&count)
	if err != nil {
		tx.Rollback(ctx)
		return
	}

	_, err = tx.Exec(ctx, "UPDATE employee SET emp_name = 'macbook' WHERE id = $1", 3)
	if err != nil {
		tx.Rollback(ctx)
		return
	}

	err = tx.Commit(ctx)
	if err != nil {
		log.Fatal(err)
	}
}

func queryStdSql(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("pgxv5-pinpoint", "postgresql://testuser:p123@localhost/testdb?sslmode=disable")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer db.Close()

	ctx := pinpoint.NewContext(context.Background(), pinpoint.TracerFromRequestContext(r))

	_, _ = db.ExecContext(ctx, "CREATE TABLE employee (id INTEGER PRIMARY KEY, emp_name VARCHAR(64), department VARCHAR(64), created DATE)")

	stmt, err := db.Prepare("INSERT INTO employee VALUES ($1, $2, $3, $4)")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}

	_, _ = stmt.ExecContext(ctx, 1, "foo", "pinpoint", "2022-08-15")
	_, _ = stmt.ExecContext(ctx, 2, "bar", "avengers", "2022-08-16")
	stmt.Close()

	stmt, err = db.PrepareContext(ctx, "UPDATE employee SET emp_name = $1 where id = $2")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}

	res, _ := stmt.ExecContext(ctx, "ironman", 2)
	_, _ = res.RowsAffected()
	stmt.Close()

	var (
		uid        int
		empName    string
		department string
		created    string
	)

	rows, _ := db.QueryContext(ctx, "SELECT * FROM employee WHERE id = 1")
	for rows.Next() {
		_ = rows.Scan(&uid, &empName, &department, &created)
		fmt.Printf("user: %d, %s, %s, %s\n", uid, empName, department, created)
	}
	rows.Close()

	//not traced
	rows, _ = db.Query("SELECT * FROM employee WHERE id = 2")
	rows.Close()

	stmt, err = db.Prepare("SELECT * FROM employee WHERE id = $1")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}

	rows, _ = stmt.QueryContext(ctx, 1)
	for rows.Next() {
		_ = rows.Scan(&uid, &empName, &department, &created)
		fmt.Printf("user: %d, %s, %s, %s\n", uid, empName, department, created)
	}
	rows.Close()
	stmt.Close()

	txStdSql(ctx, db)

	res, _ = db.ExecContext(ctx, "DROP TABLE employee")
}

func txStdSql(ctx context.Context, db *sql.DB) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}

	_, err = tx.ExecContext(ctx, "INSERT INTO employee VALUES (3, 'ipad', 'apple', '2022-08-15'), ($1, $2, $3, $4)",
		4, "chrome", "google", "2022-08-18")
	if err != nil {
		tx.Rollback()
		return
	}

	row := tx.QueryRowContext(ctx, "SELECT count(*) FROM employee")
	var count int
	err = row.Scan(&count)
	if err != nil {
		tx.Rollback()
		return
	}

	_, err = tx.ExecContext(ctx, "UPDATE employee SET emp_name = 'macbook' WHERE id = $1", 3)
	if err != nil {
		tx.Rollback()
		return
	}

	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}

}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoPgxv5Test"),
		pinpoint.WithAgentId("GoPgxv5TestId"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
		pinpoint.WithLogLevel("debug"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/tableCount", pphttp.WrapHandlerFunc(tableCount))
	http.HandleFunc("/query", pphttp.WrapHandlerFunc(query))
	http.HandleFunc("/query_std", pphttp.WrapHandlerFunc(queryStdSql))

	http.ListenAndServe(":9002", nil)
}
