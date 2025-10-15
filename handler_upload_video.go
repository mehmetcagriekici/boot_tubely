package main

import (
	"net/http"
	"errors"
	"mime"
	"os"
	"io"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"bytes"
	"encoding/json"

        "github.com/google/uuid"
        "github.com/mehmetcagriekici/boot_tubely/internal/auth"
        "github.com/aws/aws-sdk-go-v2/service/s3"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// set an upload limit of 1GB
	const maxUpload = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)

	// extract the video id from the url path and parse it as a uuid
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid videoID", err)
		return
	}

	// authenticate the user to get a user id
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't get the token", err)
		return
	}
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't validate the token", err)
		return
	}

	// get the video metadata from the database
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't fetch the video", err)
		return
	}
	// if the user is not the video owner, return a http.StatusUnauthorized response
	if userID.String() != video.UserID.String() {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized user", errors.New("Must be the video owner"))
		return
	}

	// parse the uploaded video file from the form data
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse the video file", err)
		return
	}
	defer file.Close()

	// validate the uploaded file to ensure it's an MP4 video
	mediaType := header.Header.Get("Content-Type")
	parsedMediaType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't parse the media type", err)
		return
	}
	if parsedMediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", errors.New("File must be a video/mp4 file"))
		return
	}

	// save the uploaded file to a temporary file on disk
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create a temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// copy the contents over from the wire to temp file
	if _, err := io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy the file content", err)
		return
	}

	// get the aspect ratio
	ratio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get the file aspect", err)
		return
	}

	// reset the temp file's file pointer to the beginning with .Seek(0, io.SeekStart) to allow us to read the file again from the beginning
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't reset the file pointer", err)
		return
	}

	// put the object into s3 with the bucket name, file key, file contents, and the content type
	randKey := make([]byte, 32)
	rand.Read(randKey)
	fileKey := ratio + "/" + hex.EncodeToString(randKey) + ".mp4"

        // create a processed version of the video
	outPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't process the fast start file", err)
		return
	}
	
	processedFile, err := os.Open(outPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't open the processed file", err)
		return
	}

	params := &s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key: &fileKey,
		Body: processedFile,
		ContentType: &parsedMediaType,
	}
	if _, err := cfg.s3Client.PutObject(cfg.ctx, params); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't put the file object into the s3 client", err)
		return
	}

	// update the videoURL of the video record in the database with the s3 bucket and key
	url := fmt.Sprintf("https://%s/%s", cfg.s3CfDistribution , fileKey)
	video.VideoURL = &url

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate the presigned URL", err)
		return
	}

	// update the record in the database
	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusBadRequest, "Could not update the video record", err)
		return
	}

	// send response
	respondWithJSON(w, http.StatusOK, video)
}

// function that takes a file path and returns the aspect ratio as a string
func getVideoAspectRatio(filePath string) (string, error) {
	// run the ffprobe command with the args -v, error, -pring_format, json, -show_streams, and the file path
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
        
	// set the cmd's Stdout field to a pointer to a new bytes.Buffer
        var b bytes.Buffer
	cmd.Stdout = &b

	// run the command
	if err := cmd.Run(); err != nil {
		return "", err
	}

	// unmarshal the stdout of the command from the buffer's Bytes into a JSON struct to get the width and height fields
	type Stream struct {
		Width int `json:"width"`
		Height int `json:"height"`
	}
	type Aspect struct {
		Streams []Stream `json:"streams"`
	}
	a := Aspect{}
	if err := json.Unmarshal(b.Bytes(), &a); err != nil {
		return "", err
	}

	if len(a.Streams) == 0 {
		return "", errors.New("Couldn't get the witdth and height")
	}

	ratio := float32(a.Streams[0].Width) / float32(a.Streams[0].Height)
	if ratio > 1.6 && ratio < 1.9 {
		// 16:9
		return "landscape", nil
	}
	if ratio > 0.4 && ratio < 0.6 {
		// 9:16
		return "portrait", nil
	}
	return "other", nil
}

// function that takes a file path as input and creates and returns a new path to a file with "fast start" encoding
func processVideoForFastStart(filePath string) (string, error) {
	// append .processing to the input file -temp file on the disk-
	outPath := filePath + ".processing"

	// create a ffmpeg comand with -i, input file path, -c, copy, -movflags, faststart, -f, mp4 and the output file path
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outPath)

	// run the command
	if err := cmd.Run(); err != nil {
		return "", err
	}

	// return the output path
	return outPath, nil
}
