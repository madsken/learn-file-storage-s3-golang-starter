package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

type VideoMetaData struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 10 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxMemory)

	userID, err := authenticateUser(w, r, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	videoID, err := uuid.Parse(r.PathValue("videoID"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	videoDB, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not get video from db", err)
		return
	}
	if videoDB.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "unautherized user", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "unable to get video data", err)
		return
	}
	defer file.Close()

	medType, err := validateVideoFile(header)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "invalid media file", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely_default.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not create temp video file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error saving file", err)
		return
	}

	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error resetting file pointer", err)
		return
	}

	key, err := generateBucketKey(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "unable to generate bucket key", err)
		return
	}

	processedFilePath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error creating processed video", err)
		return
	}
	defer os.Remove(processedFilePath)

	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error opening processed video", err)
		return
	}
	defer processedFile.Close()

	_, err = cfg.s3Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(key),
		Body:        processedFile,
		ContentType: aws.String(medType),
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error putting in bucket", err)
		return
	}

	newUrl := fmt.Sprintf("http://%s/%s", cfg.s3CfDistribution, key)
	videoDB.VideoURL = &newUrl
	err = cfg.db.UpdateVideo(videoDB)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "unable to update video in database", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoDB)
}

func processVideoForFastStart(filepath string) (string, error) {
	newFilePath := fmt.Sprintf("%s.process", filepath)
	cmd := exec.Command(
		"ffmpeg",
		"-i", filepath,
		"-c", "copy",
		"-movflags", "faststart",
		"-f", "mp4", newFilePath,
	)
	var buffer bytes.Buffer
	cmd.Stdout = &buffer
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return newFilePath, nil
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		filePath,
	)
	var buffer bytes.Buffer
	cmd.Stdout = &buffer
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	videoMeta := VideoMetaData{}
	err = json.Unmarshal(buffer.Bytes(), &videoMeta)
	if err != nil {
		return "", err
	}

	width := videoMeta.Streams[0].Width
	height := videoMeta.Streams[0].Height

	if float32(width)/float32(height) == 1.7777778 {
		return "16:9", nil
	}
	if float32(width)/float32(height) == 0.56296295 {
		return "9:16", nil
	}

	return "other", nil
}

func getPrefix(filepath string) (string, error) {
	aspectRatio, err := getVideoAspectRatio(filepath)
	if err != nil {
		return "", err
	}

	if aspectRatio == "16:9" {
		return "landscape", nil
	}
	if aspectRatio == "9:16" {
		return "portrait", nil
	}

	return "other", nil
}

func generateBucketKey(filepath string) (string, error) {
	random := make([]byte, 32)
	rand.Read(random)
	b64Str := base64.RawURLEncoding.EncodeToString(random)

	prefix, err := getPrefix(filepath)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s/%s.mp4", prefix, b64Str), nil
}

func validateVideoFile(header *multipart.FileHeader) (string, error) {
	medType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		return "", err
	}
	if medType != "video/mp4" {
		return "", fmt.Errorf("unsupported media type")
	}
	return medType, nil
}

func authenticateUser(w http.ResponseWriter, r *http.Request, secret string) (uuid.UUID, error) {
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		return uuid.Nil, err
	}

	userID, err := auth.ValidateJWT(token, secret)
	if err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}
