package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 << 20
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "could not parse multipart form", err)
		return
	}

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	medType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "could not parse media type", err)
		return
	}
	if medType != "image/png" && medType != "image/jpeg" {
		respondWithError(w, http.StatusBadRequest, "unsupported file type", err)
		return
	}

	videoDb, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not get video data", err)
		return
	}
	if videoDb.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "unautherized user", err)
		return
	}

	ext, err := getFileExtension(header)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "error getting file extension", err)
		return
	}

	thUrl, err := createThumbnailFileName(file, ext, cfg.assetsRoot, cfg.port)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not create file", err)
		return
	}
	videoDb.ThumbnailURL = &thUrl

	err = cfg.db.UpdateVideo(videoDb)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not save video data to database", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoDb)
}

func createThumbnailFileName(file io.Reader, ext, assetsRoot, port string) (string, error) {
	random := make([]byte, 32)
	rand.Read(random)
	b64Str := base64.RawURLEncoding.EncodeToString(random)

	thPath := fmt.Sprintf("%s.%s", b64Str, ext)
	thPath = filepath.Join(assetsRoot, thPath)

	err := createThumbnailFile(thPath, file)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("http://localhost:%s/assets/%s.%s", port, b64Str, ext), err
}

func createThumbnailFile(thPath string, file io.Reader) error {
	osFile, err := os.Create(thPath)
	if err != nil {
		return nil
	}
	defer osFile.Close()

	_, err = io.Copy(osFile, file)
	if err != nil {
		return nil
	}
	fmt.Printf("Created file at: %s\n", thPath)

	return nil
}

func getFileExtension(header *multipart.FileHeader) (string, error) {
	splitContentType := strings.Split(header.Header.Get("Content-Type"), "/")
	if len(splitContentType) != 2 {
		return "", fmt.Errorf("invalid Content-Type header")
	}
	ext := splitContentType[1]
	if ext == "jpeg" {
		ext = "jpg"
	}
	return ext, nil
}
