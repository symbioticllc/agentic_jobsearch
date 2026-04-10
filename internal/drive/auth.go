package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// Retrieve a token, saves the token, then returns the generated client.
func getClient(config *oauth2.Config) (*http.Client, error) {
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		// Because we're in a headless daemon, if token doesn't exist, we must instruct via CLI or UI
		authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
		return nil, fmt.Errorf("OAuth token not found. You must authenticate. Visit this URL in your browser: \n%v\nThen manually run the backend token generator", authURL)
	}
	return config.Client(context.Background(), tok), nil
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// UploadAsDocx takes an HTML string, authenticates with Drive, and uploads it as a native Google Doc
func UploadAsDocx(ctx context.Context, title string, htmlContent string) (string, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return "", fmt.Errorf("missing credentials.json: %w", err)
	}

	config, err := google.ConfigFromJSON(b, drive.DriveFileScope)
	if err != nil {
		return "", fmt.Errorf("unable to parse client secret file: %w", err)
	}

	client, err := getClient(config)
	if err != nil {
		return "", err // Propagates the AuthURL error to the UI
	}

	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return "", fmt.Errorf("unable to retrieve Drive client: %w", err)
	}

	tempFile, err := os.CreateTemp("", "upload-*.html")
	if err != nil {
		return "", err
	}
	defer os.Remove(tempFile.Name())
	
	if _, err := tempFile.WriteString(htmlContent); err != nil {
		return "", err
	}
	tempFile.Close()

	upFile, err := os.Open(tempFile.Name())
	if err != nil {
		return "", err
	}
	defer upFile.Close()

	// Setting mimeType to target a native Google Workspace Document
	f := &drive.File{
		Name:     title,
		MimeType: "application/vnd.google-apps.document", 
	}
	
	// Doing the multipart upload. 
	// Provide the HTML file, and let Google's ingestion converter parse it
	res, err := srv.Files.Create(f).Media(upFile, googleapi.ContentType("text/html")).Do()
	if err != nil {
		return "", fmt.Errorf("unable to upload file: %w", err)
	}

	docURL := fmt.Sprintf("https://docs.google.com/document/d/%s/edit", res.Id)
	return docURL, nil
}
