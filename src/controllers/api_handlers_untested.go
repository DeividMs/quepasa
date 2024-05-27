package controllers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	metrics "github.com/nocodeleaks/quepasa/metrics"
	models "github.com/nocodeleaks/quepasa/models"
	whatsapp "github.com/nocodeleaks/quepasa/whatsapp"
)

// ReceiveAPIHandler renders route GET "/{version}/bot/{token}/receive"
func ReceiveAPIHandler(w http.ResponseWriter, r *http.Request) {
	response := &models.QpReceiveResponse{}

	server, err := GetServer(r)
	if err != nil {
		metrics.MessageReceiveErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Checking for ready state
	status := server.GetStatus()
	if status != whatsapp.Ready {
		metrics.MessageReceiveErrors.Inc()
		err = &ApiServerNotReadyException{Wid: server.GetWId(), Status: status}
		response.ParseError(err)
		RespondInterfaceCode(w, response, http.StatusServiceUnavailable)
		return
	}

	queryValues := r.URL.Query()
	paramTimestamp := queryValues.Get("timestamp")
	timestamp, err := GetTimestamp(paramTimestamp)
	if err != nil {
		metrics.MessageReceiveErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	response.Total = uint64(server.Handler.GetTotal())

	messages := GetMessages(server, timestamp)
	metrics.MessagesReceived.Add(float64(len(messages)))

	response.Server = server.QpServer
	response.Messages = messages

	if timestamp > 0 {
		searchTime := time.Unix(timestamp, 0)
		msg := fmt.Sprintf("getting with timestamp: %v => %s", timestamp, searchTime)
		response.ParseSuccess(msg)
	} else {
		response.ParseSuccess("getting without filter")
	}

	RespondSuccess(w, response)
}

// SendAPIHandler renders route "/v3/bot/{token}/send"
func SendAny(w http.ResponseWriter, r *http.Request) {
	response := &models.QpSendResponse{}

	server, err := GetServer(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Declare a new request struct.
	request := &models.QpSendAnyRequest{}

	if r.ContentLength > 0 && r.Method == http.MethodPost {
		// Try to decode the request body into the struct. If there is an error,
		// respond to the client with the error message and a 400 status code.
		err = json.NewDecoder(r.Body).Decode(&request)
		if err != nil {
			jsonErr := fmt.Errorf("invalid json body: %s", err.Error())
			response.ParseError(jsonErr)
			RespondInterface(w, response)
			return
		}
	}

	if len(request.Text) == 0 && r.URL.Query().Has("text") {
		request.Text = r.URL.Query().Get("text")
	}

	if len(request.Url) == 0 && r.URL.Query().Has("url") {
		request.Url = r.URL.Query().Get("url")
	}

	// Getting ChatId parameter
	err = request.EnsureValidChatId(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// override trackid if passed throw any other way
	trackid := GetTrackId(r)
	if len(trackid) > 0 {
		request.TrackId = trackid
	}

	if len(request.Url) > 0 {
		// base 64 content to byte array
		err = request.GenerateUrlContent()
		if err != nil {
			metrics.MessageSendErrors.Inc()
			response.ParseError(err)
			RespondInterface(w, response)
			return
		}

		SendAttachment(server, response, &request.QpSendRequest, w)
	} else if len(request.Content) > 0 {
		// base 64 content to byte array
		err = request.GenerateEmbedContent()
		if err != nil {
			metrics.MessageSendErrors.Inc()
			response.ParseError(err)
			RespondInterface(w, response)
			return
		}

		SendAttachment(server, response, &request.QpSendRequest, w)
	} else {
		// text msg

		if len(request.Text) == 0 {
			metrics.MessageSendErrors.Inc()
			err = fmt.Errorf("text not found, do not send empty messages")
			response.ParseError(err)
			RespondInterface(w, response)
			return
		}

		Send(server, response, &request.QpSendRequest, w, nil)
	}
}

// SendAPIHandler renders route "/v3/bot/{token}/sendtext"
func SendText(w http.ResponseWriter, r *http.Request) {
	response := &models.QpSendResponse{}

	server, err := GetServer(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Declare a new request struct.
	request := &models.QpSendRequest{}

	// Try to decode the request body into the struct. If there is an error,
	// respond to the client with the error message and a 400 status code.
	err = json.NewDecoder(r.Body).Decode(request)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	if len(request.Text) == 0 {
		metrics.MessageSendErrors.Inc()
		err = fmt.Errorf("text not found, do not send empty messages")
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Getting ChatId parameter
	err = request.EnsureValidChatId(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// override trackid if passed throw any other way
	trackid := GetTrackId(r)
	if len(trackid) > 0 {
		request.TrackId = trackid
	}

	Send(server, response, request, w, nil)
}

/*
<summary>

	Renders route POST "/{version}/bot/{token}/sendbinary/{chatid}/{filename}/{text}"

	Any of then, at this order of priority
	Path parameters: {chatid}
	Path parameters: {filename}
	Path parameters: {text} only images
	Url parameters: ?chatid={chatid}
	Url parameters: ?filename={filename}
	Url parameters: ?text={text} only images
	Header parameters: X-QUEPASA-CHATID = {chatid}
	Header parameters: X-QUEPASA-FILENAME = {filename}
	Header parameters: X-QUEPASA-TEXT = {text} only images

</summary>
*/
func SendDocumentFromBinary(w http.ResponseWriter, r *http.Request) {
	response := &models.QpSendResponse{}

	server, err := GetServer(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Declare a new request struct.
	request := &models.QpSendRequest{}

	// Getting ChatId parameter
	err = request.EnsureValidChatId(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	content, err := io.ReadAll(r.Body)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(fmt.Errorf("attachment content missing or read error"))
		RespondInterface(w, response)
		return
	}

	request.Content = content
	request.Mimetype = r.Header.Get("Content-Type")

	length, err := strconv.ParseUint(r.Header.Get("Content-Length"), 10, 64)
	if err == nil {
		request.FileLength = length
	}

	// Getting FileName parameter
	filename := GetFileName(r)

	// Setting filename
	request.FileName = filename

	// Getting textLabel parameter
	text := GetTextParameter(r)

	request.Text = text

	// override trackid if passed throw any other way
	trackid := GetTrackId(r)
	if len(trackid) > 0 {
		request.TrackId = trackid
	}

	SendAttachment(server, response, request, w)
}

/*
<summary>

	Renders route POST "/{version}/bot/{token}/sendencoded"

	Body parameter: {chatid}
	Body parameter: {filename}
	Body parameter: {text} only images
	Body parameter: {content}

</summary>
*/
func SendDocumentFromEncoded(w http.ResponseWriter, r *http.Request) {
	response := &models.QpSendResponse{}

	server, err := GetServer(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Declare a new request struct.
	request := &models.QpSendRequestEncoded{}

	// Try to decode the request body into the struct. If there is an error,
	// respond to the client with the error message and a 400 status code.
	err = json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Getting ChatId parameter
	err = request.EnsureValidChatId(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// base 64 content to byte array
	err = request.GenerateContent()
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// override trackid if passed throw any other way
	trackid := GetTrackId(r)
	if len(trackid) > 0 {
		request.TrackId = trackid
	}

	SendAttachment(server, response, &request.QpSendRequest, w)
}

/*
<summary>

	Renders route POST "/{version}/bot/{token}/sendurl"

	Body parameter: {url}
	Body parameter: {chatid}
	Body parameter: {filename}
	Body parameter: {text} only images

</summary>
*/
func SendAnyFromUrl(w http.ResponseWriter, r *http.Request) {
	response := &models.QpSendResponse{}

	server, err := GetServer(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Declare a new request struct.
	request := &models.QpSendRequestUrl{}

	// Try to decode the request body into the struct. If there is an error,
	// respond to the client with the error message and a 400 status code.
	err = json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// Getting ChatId parameter
	err = request.EnsureValidChatId(r)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// url download content to byte array
	err = request.GenerateContent()
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// override trackid if passed throw any other way
	trackid := GetTrackId(r)
	if len(trackid) > 0 {
		request.TrackId = trackid
	}

	SendAttachment(server, response, &request.QpSendRequest, w)
}

func Send(server *models.QpWhatsappServer, response *models.QpSendResponse, request *models.QpSendRequest, w http.ResponseWriter, attach *whatsapp.WhatsappAttachment) {
	waMsg, err := request.ToWhatsappMessage()
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	if attach != nil {
		waMsg.Attachment = attach
		waMsg.Type = whatsapp.GetMessageType(attach)

		logentry := server.GetLogger()
		logentry.Debugf("send attachment of type: %v, mime: %s, length: %v, filename: %s", waMsg.Type, attach.Mimetype, attach.FileLength, attach.FileName)
	} else {
		// test for poll, already set from ToWhatsappMessage
		waMsg.Type = whatsapp.TextMessageType
	}

	if waMsg.Type == whatsapp.UnknownMessageType {
		// correct msg type for texts contents
		if len(waMsg.Text) > 0 {
			waMsg.Type = whatsapp.TextMessageType
		}

		// *** implement an error here if not found any knowing type
	}

	// Checking for ready state
	status := server.GetStatus()
	if status != whatsapp.Ready {
		err = &ApiServerNotReadyException{Wid: server.GetWId(), Status: status}
		response.ParseError(err)
		RespondInterfaceCode(w, response, http.StatusServiceUnavailable)
		return
	}

	sendResponse, err := server.SendMessage(waMsg)
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	// success
	metrics.MessagesSent.Inc()

	result := &models.QpSendResponseMessage{}
	result.Wid = server.GetWId()
	result.Id = sendResponse.GetId()
	result.ChatId = waMsg.Chat.Id
	result.TrackId = waMsg.TrackId

	response.ParseSuccess(result)
	RespondInterface(w, response)
}

func SendAttachment(server *models.QpWhatsappServer, response *models.QpSendResponse, request *models.QpSendRequest, w http.ResponseWriter) {
	attach, err := request.ToWhatsappAttachment()
	if err != nil {
		metrics.MessageSendErrors.Inc()
		response.ParseError(err)
		RespondInterface(w, response)
		return
	}

	Send(server, response, request, w, attach)
}
