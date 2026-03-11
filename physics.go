package main

import "math"

// 根据温度计算 Arrhenius 加速因子
func CalculateAgingFactor(currentTemp float64) float64 {
	// 假设基准温度 300K (约27度)，Ea/k = 5000 (简化参数)
	baseT := 300.0
	realT := currentTemp + 273.15 // 摄氏度转开尔文

	// Arrhenius 公式
	af := math.Exp(5000 * (1/baseT - 1/realT))
	return af
}

// 修正 Weibull 概率
func AdjustWeibullProb(baseProb float64, af float64) float64 {
	newProb := baseProb * af
	if newProb > 1.0 {
		return 1.0
	}
	return newProb
}
