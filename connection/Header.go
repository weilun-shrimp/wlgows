package connection

func headerToStr(header map[string]string) string {
	result := ""
	for k, v := range header {
		result += k + ": " + v + "\r\n"
	}
	result += "\r\n"
	return result
}
