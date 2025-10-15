package main

import (
	"time"
	"strings"
	"context"

	"github.com/mehmetcagriekici/boot_tubely/internal/database"
        "github.com/aws/aws-sdk-go-v2/service/s3"
)

func (cfg *apiConfig) _dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil || !strings.Contains(*video.VideoURL, ",") {
		return video, nil
	}

	// split the video.VideoURL on the comma to get the bucket and key
	x := strings.Split(*video.VideoURL, ",")
	bucket, key := x[0], x[1]

	// get a presigned URL for the video
	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, 100 * time.Second, cfg.ctx)
	if err != nil {
		return video, err
	}

	// update the video URL and return the updated video
	video.VideoURL = &presignedURL
	return video, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration, ctx context.Context) (string, error) {
	// create a s3.PresignClient
	presignClient := s3.NewPresignClient(s3Client)

	// get the presigned http request
	params := &s3.GetObjectInput{
		Bucket: &bucket,
		Key: &key,
	}
	presignedHTTPRequest, err := presignClient.PresignGetObject(ctx, params, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}

	// return the URL
	return presignedHTTPRequest.URL, nil
}


