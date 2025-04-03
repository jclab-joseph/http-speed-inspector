package tcpawarehttp

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"reflect"
	"unsafe"
)

type TcpAwareHandler struct {
	Handler http.Handler
}

func (h *TcpAwareHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rwc, err := getRwcFromResponse(w)
	if err != nil {
		log.Printf("Error getting rwc: %v", err)
	}
	// Serve the original handler
	reqCtx, _ := WithTcpCtx(r.Context(), rwc)
	h.Handler.ServeHTTP(w, r.WithContext(reqCtx))
}

func getRwcFromResponse(resp http.ResponseWriter) (net.Conn, error) {
	respVal := reflect.ValueOf(resp)

	if respVal.IsNil() {
		return nil, fmt.Errorf("response is nil")
	}

	// 포인터에서 실제 구조체 값 얻기
	respVal = respVal.Elem()

	// conn 필드 접근
	connField := respVal.FieldByName("conn")
	if !connField.IsValid() {
		return nil, fmt.Errorf("conn field not found")
	}

	if connField.IsNil() {
		return nil, fmt.Errorf("conn is nil")
	}

	// conn 구조체에 접근
	connVal := connField.Elem()

	// rwc 필드 접근
	rwcField := connVal.FieldByName("rwc")
	if !rwcField.IsValid() {
		return nil, fmt.Errorf("rwc field not found")
	}

	iface := getUnexportedField(rwcField)

	rwc, ok := iface.(net.Conn)
	if !ok {
		return nil, fmt.Errorf("failed to convert rwc to net.Conn")
	}

	return rwc, nil
}

// getUnexportedField 비공개 필드에 접근하기 위한 함수
func getUnexportedField(field reflect.Value) interface{} {
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface()
}
