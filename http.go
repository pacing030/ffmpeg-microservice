package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
)

type HTTPServer struct {
	ListenAddr     string
	AllowedOrigins []string
}

func NewHTTPServer(listenAddr string, allowedOrigins []string) *HTTPServer {
	return &HTTPServer{
		ListenAddr:     listenAddr,
		AllowedOrigins: allowedOrigins,
	}
}

func (hs *HTTPServer) Run() error {
	router := mux.NewRouter()
	router.HandleFunc("/", handlePost).Methods("POST", "OPTIONS")
	return http.ListenAndServe(hs.ListenAddr, withCORS(router, hs.AllowedOrigins))
}

func handlePost(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get(URLQueryParamFormData) == "1" {
		handleFormDataMultiFile(w, r)
	} else {
		handleHTTP(w, r)
	}
}

func handleHTTP(w http.ResponseWriter, r *http.Request) {
	// Read the command from the request header
	command := r.Header.Get(HTTPHeaderCommand)
	if command == "" {
		http.Error(w, "Missing command", http.StatusBadRequest)
		return
	}
	// Check if client is requesting the output to be streamed back as the response.
	// If so, the stdout of the cmd is set to w
	var stdout io.Writer = os.Stderr
	if r.Header.Get(HTTPHeaderAccept) == ContentTypeApplicationOctetStream {
		stdout = w
	}
	cmd := PrepareCmd(command, r.Body, stdout, os.Stderr)
	if err := cmd.Run(); err != nil {
		http.Error(w, fmt.Sprintf("command failed: %v", err), http.StatusInternalServerError)
	}
}

// handleFormData: ORIGINAL - kept for backwards compatibility
func handleFormData(w http.ResponseWriter, r *http.Request) {
	// Parse the multipart form
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "Failed to parse multipart form", http.StatusBadRequest)
		return
	}
	// Get the command from the form data
	command := r.FormValue(FormDataKeyCommand)
	if command == "" {
		http.Error(w, "Missing command", http.StatusBadRequest)
		return
	}
	// Get the input file from the form data
	file, _, err := r.FormFile(FormDataKeyFile)
	if err != nil {
		http.Error(w, "Failed to get input file", http.StatusBadRequest)
		return
	}
	defer file.Close()
	cmd := PrepareCmd(command, file, w, os.Stderr)
	if err := cmd.Run(); err != nil {
		http.Error(w, fmt.Sprintf("command failed: %v", err), http.StatusInternalServerError)
	}
}

// handleFormDataMultiFile: NEW - supports multiple files
func handleFormDataMultiFile(w http.ResponseWriter, r *http.Request) {
	// Parse the multipart form with 100MB max memory
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "Failed to parse multipart form", http.StatusBadRequest)
		return
	}

	// Get the command from the form data AFTER parsing
	command := r.FormValue("command")
	if command == "" {
		http.Error(w, fmt.Sprintf("Missing command (available fields: %v)", r.MultipartForm.Value), http.StatusBadRequest)
		return
	}

	// Create a temporary directory for all files
	tmpDir, err := os.MkdirTemp("", "ffmpeg-*")
	if err != nil {
		http.Error(w, "Failed to create temp directory", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir) // Clean up after FFmpeg completes

	// Handle multiple files from the multipart form
	fileCount := 0
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		for fieldName, fileHeaders := range r.MultipartForm.File {
			for i, fileHeader := range fileHeaders {
				file, err := fileHeader.Open()
				if err != nil {
					http.Error(w, "Failed to open uploaded file", http.StatusBadRequest)
					return
				}
				defer file.Close()

				// Create unique filename for this file
				var tempFilePath string
				if len(fileHeaders) == 1 {
					tempFilePath = filepath.Join(tmpDir, fieldName)
				} else {
					tempFilePath = filepath.Join(tmpDir, fmt.Sprintf("%s_%d%s", fieldName, i, filepath.Ext(fileHeader.Filename)))
				}

				// Write uploaded file to temp location
				tempFile, err := os.Create(tempFilePath)
				if err != nil {
					http.Error(w, "Failed to create temp file", http.StatusInternalServerError)
					return
				}

				if _, err := io.Copy(tempFile, file); err != nil {
					tempFile.Close()
					http.Error(w, "Failed to save uploaded file", http.StatusInternalServerError)
					return
				}
				tempFile.Close()

				// Replace placeholders in command with actual file paths
				// Supports: <fieldname>, <fieldname_0>, <fieldname_1>, etc.
				placeholder := fmt.Sprintf("<%s>", fieldName)
				command = replaceAll(command, placeholder, tempFilePath)

				fileCount++
			}
		}
	}

	// Fallback for legacy single "file" field if no files were found
	if fileCount == 0 {
		file, _, err := r.FormFile(FormDataKeyFile)
		if err == nil {
			defer file.Close()
			tempFilePath := filepath.Join(tmpDir, "input")
			tempFile, err := os.Create(tempFilePath)
			if err != nil {
				http.Error(w, "Failed to create temp file", http.StatusInternalServerError)
				return
			}
			defer tempFile.Close()
			io.Copy(tempFile, file)

			placeholder := fmt.Sprintf("<%s>", FormDataKeyFile)
			command = replaceAll(command, placeholder, tempFilePath)
		}
	}

	// Check if client is requesting the output to be streamed back as the response
	var stdout io.Writer = os.Stderr
	if r.Header.Get(HTTPHeaderAccept) == ContentTypeApplicationOctetStream {
		stdout = w
	}

	// Prepare and run the FFmpeg command
	cmd := PrepareCmd(command, nil, stdout, os.Stderr)
	if err := cmd.Run(); err != nil {
		http.Error(w, fmt.Sprintf("command failed: %v", err), http.StatusInternalServerError)
	}
}

// contains checks if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0)
}

// replaceAll replaces all occurrences of old with new in s
func replaceAll(s, old, new string) string {
	for {
		i := 0
		for i <= len(s)-len(old) {
			if s[i:i+len(old)] == old {
				s = s[:i] + new + s[i+len(old):]
				i += len(new)
				break
			}
			i++
		}
		if i == 0 || i > len(s)-len(old) {
			break
		}
	}
	return s
}

func withCORS(handler http.Handler, allowedOrigins []string) http.Handler {
	headersOk := handlers.AllowedHeaders([]string{HTTPHeaderAccept, HTTPHeaderCommand, HTTPHeaderContentType})
	methodsOk := handlers.AllowedMethods([]string{"POST", "OPTIONS"})
	originsOk := handlers.AllowedOrigins(allowedOrigins)
	return handlers.CORS(originsOk, methodsOk, headersOk)(handler)
}
