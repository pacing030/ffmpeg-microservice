package main

import (
	"fmt"
	"io"
	"log"
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
	log.Printf("=== handlePost called ===")
	log.Printf("URL: %s", r.URL.String())
	log.Printf("Query params: %s", r.URL.Query().Encode())
	log.Printf("Method: %s", r.Method)
	log.Printf("Content-Type: %s", r.Header.Get("Content-Type"))
	
	formDataParam := r.URL.Query().Get(URLQueryParamFormData)
	log.Printf("URLQueryParamFormData value: '%s'", formDataParam)
	
	if formDataParam == "1" {
		log.Println("→ Routing to handleFormDataMultiFile")
		handleFormDataMultiFile(w, r)
	} else {
		log.Println("→ Routing to handleHTTP")
		handleHTTP(w, r)
	}
}

func handleHTTP(w http.ResponseWriter, r *http.Request) {
	log.Println("=== handleHTTP called ===")
	
	// Read the command from the request header
	command := r.Header.Get(HTTPHeaderCommand)
	log.Printf("Command from header: '%s'", command)
	
	if command == "" {
		log.Println("ERROR: Missing command header")
		http.Error(w, "Missing command", http.StatusBadRequest)
		return
	}
	
	// Check if client is requesting the output to be streamed back as the response.
	var stdout io.Writer = os.Stderr
	if r.Header.Get(HTTPHeaderAccept) == ContentTypeApplicationOctetStream {
		stdout = w
	}
	
	cmd := PrepareCmd(command, r.Body, stdout, os.Stderr)
	if err := cmd.Run(); err != nil {
		log.Printf("ERROR: Command failed: %v", err)
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
	// Catch panics to prevent server crash
	defer func() {
		if err := recover(); err != nil {
			log.Printf("❌ PANIC in handleFormDataMultiFile: %v", err)
			http.Error(w, fmt.Sprintf("Internal server error: %v", err), http.StatusInternalServerError)
		}
	}()
	
	log.Println("=== handleFormDataMultiFile called ===")
	
	// Parse the multipart form with 100MB max memory
	log.Println("Parsing multipart form...")
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		log.Printf("ERROR: Failed to parse multipart form: %v", err)
		http.Error(w, "Failed to parse multipart form", http.StatusBadRequest)
		return
	}
	log.Println("✓ Multipart form parsed successfully")

	// Get the command from the form data AFTER parsing
	command := r.FormValue("command")
	log.Printf("Command from form: '%s'", command)
	
	if command == "" {
		log.Printf("ERROR: Missing command. Available fields: %v", r.MultipartForm.Value)
		http.Error(w, fmt.Sprintf("Missing command (available fields: %v)", r.MultipartForm.Value), http.StatusBadRequest)
		return
	}

	// Create a temporary directory for all files
	tmpDir, err := os.MkdirTemp("", "ffmpeg-*")
	if err != nil {
		log.Printf("ERROR: Failed to create temp directory: %v", err)
		http.Error(w, "Failed to create temp directory", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir)
	log.Printf("✓ Created temp directory: %s", tmpDir)

	// Define output path
	outputPath := filepath.Join(tmpDir, "output.mp4")
	
	// Replace <output> placeholder with actual output path
	command = replaceAll(command, "<output>", outputPath)

	// Handle multiple files from the multipart form
	fileCount := 0
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		log.Printf("Processing %d file fields...", len(r.MultipartForm.File))
		
		for fieldName, fileHeaders := range r.MultipartForm.File {
			log.Printf("  Field '%s': %d file(s)", fieldName, len(fileHeaders))
			
			for i, fileHeader := range fileHeaders {
				file, err := fileHeader.Open()
				if err != nil {
					log.Printf("ERROR: Failed to open uploaded file: %v", err)
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
					log.Printf("ERROR: Failed to create temp file: %v", err)
					http.Error(w, "Failed to create temp file", http.StatusInternalServerError)
					return
				}

				if _, err := io.Copy(tempFile, file); err != nil {
					tempFile.Close()
					log.Printf("ERROR: Failed to save uploaded file: %v", err)
					http.Error(w, "Failed to save uploaded file", http.StatusInternalServerError)
					return
				}
				tempFile.Close()
				log.Printf("    ✓ Saved to: %s", tempFilePath)

				// Replace placeholders in command with actual file paths
				placeholder := fmt.Sprintf("<%s>", fieldName)
				command = replaceAll(command, placeholder, tempFilePath)
				log.Printf("    ✓ Replaced placeholder %s", placeholder)

				fileCount++
			}
		}
	}

	// Fallback for legacy single "file" field if no files were found
	if fileCount == 0 {
		log.Println("No files found in multipart form, trying legacy 'file' field...")
		file, _, err := r.FormFile(FormDataKeyFile)
		if err == nil {
			defer file.Close()
			tempFilePath := filepath.Join(tmpDir, "input")
			tempFile, err := os.Create(tempFilePath)
			if err != nil {
				log.Printf("ERROR: Failed to create temp file: %v", err)
				http.Error(w, "Failed to create temp file", http.StatusInternalServerError)
				return
			}
			defer tempFile.Close()
			io.Copy(tempFile, file)

			placeholder := fmt.Sprintf("<%s>", FormDataKeyFile)
			command = replaceAll(command, placeholder, tempFilePath)
			log.Printf("✓ Legacy file saved and placeholder replaced")
		} else {
			log.Printf("WARNING: No files uploaded at all: %v", err)
		}
	}

	log.Printf("Total files processed: %d", fileCount)
	log.Printf("Final command: %s", command)
	log.Printf("Expected output file: %s", outputPath)

	// Prepare and run the FFmpeg command
	log.Println("Executing FFmpeg command...")
	cmd := PrepareCmd(command, nil, os.Stderr, os.Stderr)
	if err := cmd.Run(); err != nil {
		log.Printf("❌ ERROR: Command failed: %v", err)
		http.Error(w, fmt.Sprintf("command failed: %v", err), http.StatusInternalServerError)
		return
	}
	log.Println("✓ FFmpeg command completed successfully")

	// Check if output file was created
	if _, err := os.Stat(outputPath); err != nil {
		log.Printf("ERROR: Output file not found at %s: %v", outputPath, err)
		http.Error(w, "Output file was not created", http.StatusInternalServerError)
		return
	}

	// Read the output file
	log.Printf("Reading output file: %s", outputPath)
	outputData, err := os.ReadFile(outputPath)
	if err != nil {
		log.Printf("ERROR: Failed to read output file: %v", err)
		http.Error(w, "Failed to read output file", http.StatusInternalServerError)
		return
	}

	// Send the file back to the client
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", "attachment; filename=output.mp4")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(outputData); err != nil {
		log.Printf("ERROR: Failed to write response: %v", err)
		return
	}
	
	log.Printf("✓ Successfully sent output file (%d bytes)", len(outputData))
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
