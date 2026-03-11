package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// StorageClient 封装 MinIO 客户端
type StorageClient struct {
	Client *minio.Client
	Bucket string
}

var GlobalStorage *StorageClient

// InitStorage 初始化 MinIO 连接
func InitStorage() {
	endpoint := "localhost:9000"
	accessKeyID := "minioadmin"
	secretAccessKey := "minioadmin"
	useSSL := false

	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		log.Fatalln(err)
	}

	GlobalStorage = &StorageClient{
		Client: minioClient,
		Bucket: "industrial-images",
	}

	err = GlobalStorage.EnsureBucket()
	if err != nil {
		log.Printf("Bucket error: %v", err)
	}
	log.Println("✅ MinIO Storage Connected!")
}

func (s *StorageClient) EnsureBucket() error {
	ctx := context.Background()
	exists, err := s.Client.BucketExists(ctx, s.Bucket)
	if err != nil {
		return err
	}
	if !exists {
		return s.Client.MakeBucket(ctx, s.Bucket, minio.MakeBucketOptions{})
	}
	return nil
}

// UploadFile 上传本地文件 (保留旧方法，兼容性)
func (s *StorageClient) UploadFile(objectName string, filePath string) (string, error) {
	ctx := context.Background()
	contentType := "application/octet-stream"
	ext := filepath.Ext(filePath)
	if ext == ".jpg" {
		contentType = "image/jpeg"
	}

	info, err := s.Client.FPutObject(ctx, s.Bucket, objectName, filePath, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("http://localhost:9000/%s/%s", s.Bucket, objectName)
	log.Printf("Uploaded %s size %d", objectName, info.Size)
	return url, nil
}

// UploadStream: 从内存流上传，不落地磁盘
func (s *StorageClient) UploadStream(objectName string, reader io.Reader, size int64, contentType string) (string, error) {
	ctx := context.Background()

	// PutObject 需要 reader。如果 size 未知，传 -1 (MinIO 会自动分片，但对于小图片建议尽量预估或允许分片)
	info, err := s.Client.PutObject(ctx, s.Bucket, objectName, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("http://localhost:9000/%s/%s", s.Bucket, objectName)
	log.Printf("🌊 [Stream] Uploaded %s size %d", objectName, info.Size)
	return url, nil
}

// 全局包装
func UploadFrame(objectName string, filePath string) (string, error) {
	if GlobalStorage == nil {
		return "", fmt.Errorf("Storage未初始化")
	}
	return GlobalStorage.UploadFile(objectName, filePath)
}
