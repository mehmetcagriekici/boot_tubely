package main

import (
	"fmt"
	"net/http"
	"errors"
	"strings"
	"io"
	"os"
	"path/filepath"
	"mime"
	"crypto/rand"
	"encoding/hex"

	"github.com/mehmetcagriekici/boot_tubely/internal/auth"
     	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	// authentication
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
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate the token", err)
		return
	}


	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// set the maxMemory to 10mb
	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)
	
	// get the image data and file headers
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse the image", err)
		return
	}
	defer file.Close()

	// get the media type from the form file's Content-Type header
	mediaType := header.Header.Get("Content-Type")
	parsedMediaType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
	        respondWithError(w, http.StatusBadRequest, "invalid Content-Type", err)
		return
	}

	// allow only jpeg or png
	if parsedMediaType != "image/jpeg" && parsedMediaType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "File must be a jpeg or png", errors.New("invalid file type"))
		return
	}

	// get the video's metadata from the database
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Unable to fetch the video", err)
		return
	}

	if userID.String() != video.UserID.String() {
		respondWithError(w, http.StatusUnauthorized, "Must be the video owner", errors.New("Unauthorized video access"))
		return
	}
	
	// determine the file extension
	subType := strings.Split(parsedMediaType, "/")
	extension := subType[1]
	fileExtension := fmt.Sprintf(".%s", extension)

	// create a unique string
	randKey := make([]byte, 32)
	rand.Read(randKey)
	randID := hex.EncodeToString(randKey)

	// create a unique file path
	filePath := filepath.Join(cfg.assetsRoot, randID + fileExtension)

	// create a new file
	newFile, err := os.Create(filePath)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Could not create the file", err)
		return
	}

	// copy the contents to the new file
	if _, err := io.Copy(newFile, file); err != nil {
		respondWithError(w, http.StatusBadRequest, "Could not copy the file content", err)
	}
	defer newFile.Close()
	
	// update the thumbnail url
	url := fmt.Sprintf("http://localhost:%s/assets/%s%s", cfg.port, randID, fileExtension)
	video.ThumbnailURL = &url


	// update the record in the database
	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusBadRequest, "Could not update the video record", err)
		return
	}

	// send response
	respondWithJSON(w, http.StatusOK, video)
}
