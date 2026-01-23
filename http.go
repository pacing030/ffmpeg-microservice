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
		handleFormData(w, r)
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

func handleFormData(w http.ResponseWriter, r *http.Request) {
	// Parse the multipart form with 100MB max memory
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "Failed to parse multipart form", http.StatusBadRequest)
		return
	}
	
	// Get the command from the form data
	command := r.FormValue(FormDataKeyCommand)
	if command == "" {
		http.Error(w, "Missing command", http.StatusBadRequest)
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
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		// Save all uploaded files to temp directory
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
				if len(fileHeaders) == 1 {
					command = replaceInCommand(command, fieldName, tempFilePath)
				} else {
					command = replaceInCommand(command, fmt.Sprintf("%s_%d", fieldName, i), tempFilePath)
				}
			}
		}
	}
	
	// If there's a single legacy "file" field, handle it directly via r.Body for backwards compatibility
	// (This maintains compatibility with single-file uploads via request body)
	if len(r.MultipartForm.File) == 0 {
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
			command = replaceInCommand(command, "input", tempFilePath)
		}
	}
	
	// Prepare and run the FFmpeg command
	// Use nil for stdin since all inputs are now files on disk
	cmd := PrepareCmd(command, nil, w, os.Stderr)
	if err := cmd.Run(); err != nil {
		http.Error(w, fmt.Sprintf("command failed: %v", err), http.StatusInternalServerError)
	}
}

// replaceInCommand replaces placeholder or field name with actual file path in command
func replaceInCommand(command, fieldName, filePath string) string {
	// Try replacing <fieldname> placeholder first
	placeholder := fmt.Sprintf("<%s>", fieldName)
	if contains := has(command, placeholder); contains {
		return replaceAll(command, placeholder, filePath)
	}
	return command
}

// has checks if string contains substring
func has(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0))
}

// replaceAll replaces all occurrences of old with new in s
func replaceAll(s, old, new string) string {
	var result string
	for {
		i := 0
		for i < len(s) {
			if i+len(old) <= len(s) && s[i:i+len(old)] == old {
				result += new
				i += len(old)
				break
			}
			result += string(s[i])
			i++
		}
		if i >= len(s) {
			break
		}
		s = s[i:]
	}
	return result
}

func withCORS(handler http.Handler, allowedOrigins []string) http.Handler {
	headersOk := handlers.AllowedHeaders([]string{HTTPHeaderAccept, HTTPHeaderCommand, HTTPHeaderContentType})
	methodsOk := handlers.AllowedMethods([]string{"POST", "OPTIONS"})
	originsOk := handlers.AllowedOrigins(allowedOrigins)
	return handlers.CORS(originsOk, methodsOk, headersOk)(handler)
}
