# 反应釜 MES 数据集成引擎 / Reactor MES Data Integration Engine

[![Language](https://img.shields.io/badge/Language-Go%201.20+-00ADD8.svg)](https://go.dev/)
[![Infrastructure](https://img.shields.io/badge/Infrastructure-Docker%20Compose-2496ED.svg)](https://www.docker.com/)
[![MessageBroker](https://img.shields.io/badge/EventStreaming-Kafka-231F20.svg)](https://kafka.apache.org/)
[![VectorDB](https://img.shields.io/badge/VectorDB-Milvus-0D6EFD.svg)](https://milvus.io/)

## 📖 简介 / Introduction

本项目是 配制反应釜数字孪生系统的**核心数据引擎**。采用 Go 语言编写，负责与底层工业设备（Modbus TCP）进行实时通讯，并通过 Kafka 消息总线将传感器遥测数据、报警事件流式分发至多模态存储矩阵（时序、图、关系、向量、对象存储），为上层 HMI 界面与 AI 预测模型提供毫秒级的数据支撑。

---

## 🚀 核心架构与功能 / Core Architecture & Features

### 1. 工业协议解析与仿真 (Industrial Protocol & Simulation)
* **Modbus TCP Client**: 实时采集反应釜温度与质量流量数据。
* **Virtual Reactor FSM**: 内置基于有限状态机 (FSM) 的虚拟反应釜，模拟真实生产工序（加热、保温、冷却、冷排）及偶发异常（如流量激增）。

### 2. 多模态数据路由 (Multi-modal Data Routing)
根据数据特性自动路由至最佳存储介质：
* **InfluxDB**: 存储高频连续的时序数据（温度、流量）。
* **MySQL**: 记录结构化的报警日志与批次报告。
* **MinIO**: 存储异常发生时抓拍的现场图像与非结构化文件。
* **Milvus (Vector DB)**: 存储设备状态特征向量，为 AI 相似度检索与故障溯源提供基础。

### 3. 流式事件总线 (Event Streaming Bus)
* 集成 **Apache Kafka (KRaft Mode)**，实现了 `raw_telemetry`、`flow_telemetry`、`alarm_events` 等多个主题的异步解耦。

---

## 🐳 基础设施矩阵 / Infrastructure Matrix

本项目依赖一套完整的微服务基础设施，通过 `docker-compose.yml` 一键编排：

| 组件分类 | 服务名称 | 端口 | 核心作用 |
| :--- | :--- | :--- | :--- |
| **消息中间件** | Kafka | `9092` | 核心消息总线，解耦采集与消费侧 |
| **时序数据库** | InfluxDB | `8086` | 存储设备状态的高频时序快照 |
| **关系数据库** | MySQL | `3306` | 存储报警日志与设备元数据 |
| **向量数据库** | Milvus + Etcd | `19530` | 存储多维传感器向量，支持 AI 相似性检索 |
| **对象存储** | MinIO | `9000` | 存储报警抓拍图像与视觉分析结果 |
| **缓存与队列** | Redis | `6379` | 为 AI 任务分配提供高速缓冲队列 |

---

## 🏁 快速开始 / Quick Start

### 1. 启动基础设施环境 (Start Infrastructure)
请确保本地已安装 Docker 与 Docker Compose。在项目根目录下执行：
```bash
docker-compose up -d
```
2. 初始化 Go 环境 (Init Go Environment)
```
go mod tidy
```
3. 启动引擎 (Run Engine)
```
# 终端 1: 启动 Modbus 虚拟服务器
go run mock/mock_device.go

# 终端 2: 启动主采集引擎
go run main.go db_writer.go api.go collector.go physics.go processor.go slicer.go storage.go video.go
```
🛡️ 开发者 / Developer
Author: danny95928
Project: Thesis Project - MES Go Engine Backend
