package db

import (
	"database/sql"
	"log"

	_ "github.com/mattn/go-sqlite3"
)

// DBWrapper handles the SQLite connection
type DBWrapper struct {
	DB *sql.DB
}

// Connect initializes the connection to the SQLite database
func Connect(filepath string) *DBWrapper {
	db, err := sql.Open("sqlite3", filepath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	// Ping to verify connection
	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	return &DBWrapper{DB: db}
}

// Close gracefully closes the database connection
func (w *DBWrapper) Close() {
	if w.DB != nil {
		w.DB.Close()
	}
}
