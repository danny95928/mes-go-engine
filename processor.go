package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Point3D 存储坐标
type Point3D struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// ModelData 定义传输格式
type ModelData struct {
	Points []Point3D `json:"points"`
	Count  int       `json:"count"`
}

// ProcessOBJ 读取并执行等间隔抽稀
func ProcessOBJ(filePath string, sampleRate int) (ModelData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return ModelData{}, err
	}
	defer file.Close()

	var allPoints []Point3D
	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		// 识别顶点行
		if strings.HasPrefix(line, "v ") {
			lineCount++
			if lineCount%sampleRate == 0 {
				var x, y, z float64
				// 这里使用了 fmt，修复 "not used" 错误
				_, err := fmt.Sscanf(line, "v %f %f %f", &x, &y, &z)
				if err == nil {
					allPoints = append(allPoints, Point3D{X: x, Y: y, Z: z})
				}
			}
		}
	}
	return ModelData{Points: allPoints, Count: len(allPoints)}, scanner.Err()
}

// GetModelHandler 供 api.go 调用
func GetModelHandler() string {
	path := "E:/Thesis/Reactor_HMI/model/reactor.obj"
	// 40倍抽稀：将 400万点降至 10万点，防止 Python 闪退
	data, err := ProcessOBJ(path, 40)
	if err != nil {
		return fmt.Sprintf(`{"error": "%s"}`, err.Error())
	}
	jsonData, _ := json.Marshal(data)
	return string(jsonData)
}
