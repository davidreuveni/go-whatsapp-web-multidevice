package usecase

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/disintegration/imaging"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

type serviceMessage struct {
	chatStorageRepo domainChatStorage.IChatStorageRepository
}

func NewMessageService(chatStorageRepo domainChatStorage.IChatStorageRepository) domainMessage.IMessageUsecase {
	return &serviceMessage{
		chatStorageRepo: chatStorageRepo,
	}
}

func (service serviceMessage) MarkAsRead(ctx context.Context, request domainMessage.MarkAsReadRequest) (response domainMessage.GenericResponse, err error) {
	if err = validations.ValidateMarkAsRead(ctx, request); err != nil {
		return response, err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}

	dataWaRecipient, err := utils.ValidateJidWithLogin(client, request.Phone)
	if err != nil {
		return response, err
	}

	ids := []types.MessageID{request.MessageID}
	if err = client.MarkRead(ctx, ids, time.Now(), dataWaRecipient, *client.Store.ID); err != nil {
		return response, err
	}

	logrus.Info(map[string]any{
		"phone":      request.Phone,
		"message_id": request.MessageID,
		"chat":       dataWaRecipient.String(),
		"sender":     client.Store.ID.String(),
	})

	response.MessageID = request.MessageID
	response.Status = fmt.Sprintf("Mark as read success %s", request.MessageID)
	return response, nil
}

func (service serviceMessage) ReactMessage(ctx context.Context, request domainMessage.ReactionRequest) (response domainMessage.GenericResponse, err error) {
	if err = validations.ValidateReactMessage(ctx, request); err != nil {
		return response, err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}

	dataWaRecipient, err := utils.ValidateJidWithLogin(client, request.Phone)
	if err != nil {
		return response, err
	}

	// Determine the sender of the original message for BuildReaction.
	// BuildReaction uses BuildMessageKey internally, which correctly sets the
	// Participant field for group chats — required by the WhatsApp protocol.
	// An empty JID means "message was from me".
	senderJID := types.EmptyJID
	message, err := service.chatStorageRepo.GetMessageByID(request.MessageID)
	if err != nil {
		logrus.Warnf("Failed to lookup message %s for reaction: %v, using heuristic", request.MessageID, err)
		if len(request.MessageID) > 22 {
			if dataWaRecipient.Server == types.GroupServer {
				logrus.Warnf("Cannot determine original sender for group reaction to %s — reaction may not be delivered", request.MessageID)
			}
		}
	} else if message != nil {
		if !message.IsFromMe && message.Sender != "" {
			parsed, parseErr := utils.ParseJID(message.Sender)
			if parseErr == nil {
				senderJID = parsed
			} else {
				logrus.Warnf("Failed to parse sender JID '%s' for reaction: %v", message.Sender, parseErr)
			}
		}
	} else {
		logrus.Debugf("Message %s not found in database, assuming sent by me", request.MessageID)
	}

	// BuildReaction correctly constructs the MessageKey with Participant field
	// for group chats, which is required for the reaction to be delivered.
	msg := client.BuildReaction(dataWaRecipient, senderJID, request.MessageID, request.Emoji)
	ts, err := client.SendMessage(ctx, dataWaRecipient, msg)
	if err != nil {
		return response, err
	}

	response.MessageID = ts.ID
	response.Status = fmt.Sprintf("Reaction sent to %s (server timestamp: %s)", request.Phone, ts.Timestamp)
	return response, nil
}

func (service serviceMessage) RevokeMessage(ctx context.Context, request domainMessage.RevokeRequest) (response domainMessage.GenericResponse, err error) {
	if err = validations.ValidateRevokeMessage(ctx, request); err != nil {
		return response, err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}

	dataWaRecipient, err := utils.ValidateJidWithLogin(client, request.Phone)
	if err != nil {
		return response, err
	}

	// Resolve the original sender so group admins can revoke other members'
	// messages. BuildRevoke treats types.EmptyJID as "message was from me";
	// any other JID is admin-revoke and requires the bot to be group admin.
	// WhatsApp message IDs are globally unique, so a cross-device lookup
	// via GetMessageByID yields the same sender regardless of which device
	// owns the row.
	senderJID := types.EmptyJID
	message, lookupErr := service.chatStorageRepo.GetMessageByID(request.MessageID)
	if lookupErr != nil {
		logrus.Warnf("Failed to lookup message %s for revoke: %v, assuming self-revoke", request.MessageID, lookupErr)
	} else if message != nil && !message.IsFromMe && message.Sender != "" {
		parsed, parseErr := utils.ParseJID(message.Sender)
		if parseErr != nil {
			logrus.Warnf("Failed to parse sender JID '%s' for revoke: %v", message.Sender, parseErr)
		} else {
			// Stored senders can still be @lid; whatsmeow's Revoke needs
			// the phone-number form or it rejects the request at the wire.
			senderJID = whatsapp.NormalizeJIDFromLID(ctx, parsed, client)
		}
	}

	ts, err := client.SendMessage(ctx, dataWaRecipient, client.BuildRevoke(dataWaRecipient, senderJID, request.MessageID))
	if err != nil {
		return response, err
	}

	response.MessageID = ts.ID
	response.Status = fmt.Sprintf("Revoke success %s (server timestamp: %s)", request.Phone, ts.Timestamp)
	return response, nil
}

func (service serviceMessage) DeleteMessage(ctx context.Context, request domainMessage.DeleteRequest) (err error) {
	if err = validations.ValidateDeleteMessage(ctx, request); err != nil {
		return err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return pkgError.ErrWaCLI
	}

	dataWaRecipient, err := utils.ValidateJidWithLogin(client, request.Phone)
	if err != nil {
		return err
	}

	isFromMe := "1"
	if len(request.MessageID) > 22 {
		isFromMe = "0"
	}

	patchInfo := appstate.PatchInfo{
		Timestamp: time.Now(),
		Type:      appstate.WAPatchRegularHigh,
		Mutations: []appstate.MutationInfo{{
			Index: []string{appstate.IndexDeleteMessageForMe, dataWaRecipient.String(), request.MessageID, isFromMe, client.Store.ID.String()},
			Value: &waSyncAction.SyncActionValue{
				DeleteMessageForMeAction: &waSyncAction.DeleteMessageForMeAction{
					DeleteMedia:      proto.Bool(true),
					MessageTimestamp: proto.Int64(time.Now().UnixMilli()),
				},
			},
		}},
	}

	if err = client.SendAppState(ctx, patchInfo); err != nil {
		return err
	}
	return nil
}

func (service serviceMessage) UpdateMessage(ctx context.Context, request domainMessage.UpdateMessageRequest) (response domainMessage.GenericResponse, err error) {
	if err = validations.ValidateUpdateMessage(ctx, request); err != nil {
		return response, err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}

	dataWaRecipient, err := utils.ValidateJidWithLogin(client, request.Phone)
	if err != nil {
		return response, err
	}

	msg := &waE2E.Message{Conversation: proto.String(request.Message)}
	ts, err := client.SendMessage(ctx, dataWaRecipient, client.BuildEdit(dataWaRecipient, request.MessageID, msg))
	if err != nil {
		return response, err
	}

	response.MessageID = ts.ID
	response.Status = fmt.Sprintf("Update message success %s (server timestamp: %s)", request.Phone, ts.Timestamp)
	return response, nil
}

func (service serviceMessage) ForwardMessage(ctx context.Context, request domainMessage.ForwardRequest) (response domainMessage.GenericResponse, err error) {
	if err = validations.ValidateForwardMessage(ctx, request); err != nil {
		return response, err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}

	dataWaRecipient, err := utils.ValidateJidWithLogin(client, request.ChatID)
	if err != nil {
		return response, err
	}

	storedMessage, err := service.chatStorageRepo.GetMessageByID(request.MessageID)
	if err != nil {
		return response, fmt.Errorf("message not found: %v", err)
	}
	if storedMessage == nil {
		return response, fmt.Errorf("message with ID %s not found", request.MessageID)
	}

	markAsForwarded := true
	if request.IsForwarded != nil {
		markAsForwarded = *request.IsForwarded
	}

	thumbnail := generateForwardPreviewThumbnail(ctx, client, storedMessage)
	msg, content, err := buildForwardMessageProto(storedMessage, markAsForwarded, thumbnail)
	if err != nil {
		return response, err
	}

	ts, err := sendForwardMessageWithRetry(ctx, client, dataWaRecipient, msg)
	if err != nil {
		return response, fmt.Errorf("failed to forward message %s to %s (media_type=%s has_preview=%t): %w", request.MessageID, request.ChatID, storedMessage.MediaType, len(thumbnail) > 0, err)
	}

	go service.storeForwardedMessage(ctx, client, dataWaRecipient, ts.ID, content, ts.Timestamp, msg)

	logrus.Info(map[string]any{
		"source_message_id": request.MessageID,
		"message_id":        ts.ID,
		"chat":              dataWaRecipient.String(),
		"media_type":        storedMessage.MediaType,
		"is_forwarded":      markAsForwarded,
		"has_preview":       len(thumbnail) > 0,
	})

	response.MessageID = ts.ID
	response.Status = fmt.Sprintf("Message forwarded to %s (server timestamp: %s)", request.ChatID, ts.Timestamp)
	return response, nil
}

// StarMessage implements message.IMessageService.
func (service serviceMessage) StarMessage(ctx context.Context, request domainMessage.StarRequest) (err error) {
	if err = validations.ValidateStarMessage(ctx, request); err != nil {
		return err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return pkgError.ErrWaCLI
	}

	dataWaRecipient, err := utils.ValidateJidWithLogin(client, request.Phone)
	if err != nil {
		return err
	}

	isFromMe := true
	if len(request.MessageID) > 22 {
		isFromMe = false
	}

	patchInfo := appstate.BuildStar(dataWaRecipient.ToNonAD(), *client.Store.ID, request.MessageID, isFromMe, request.IsStarred)

	if err = client.SendAppState(ctx, patchInfo); err != nil {
		return err
	}
	return nil
}

// DownloadMedia implements message.IMessageService.
func (service serviceMessage) DownloadMedia(ctx context.Context, request domainMessage.DownloadMediaRequest) (response domainMessage.DownloadMediaResponse, err error) {
	if err = validations.ValidateDownloadMedia(ctx, request); err != nil {
		return response, err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}

	dataWaRecipient, err := utils.ValidateJidWithLogin(client, request.Phone)
	if err != nil {
		return response, err
	}

	// Query the message from chat storage
	message, err := service.chatStorageRepo.GetMessageByID(request.MessageID)
	if err != nil {
		return response, fmt.Errorf("message not found: %v", err)
	}

	if message == nil {
		return response, fmt.Errorf("message with ID %s not found", request.MessageID)
	}

	// Check if message has media
	if message.MediaType == "" || message.URL == "" {
		return response, fmt.Errorf("message %s does not contain downloadable media", request.MessageID)
	}

	// Verify the message is from the specified chat
	if message.ChatJID != dataWaRecipient.String() {
		return response, fmt.Errorf("message %s does not belong to chat %s", request.MessageID, dataWaRecipient.String())
	}

	// Create directory structure for organized storage
	chatDir := filepath.Join(config.PathMedia, utils.ExtractPhoneNumber(message.ChatJID))
	dateDir := filepath.Join(chatDir, message.Timestamp.Format("2006-01-02"))

	err = os.MkdirAll(dateDir, 0755)
	if err != nil {
		return response, fmt.Errorf("failed to create directory: %v", err)
	}

	// Create a downloadable message interface based on media type
	var downloadableMsg any

	switch message.MediaType {
	case "image":
		downloadableMsg = &waE2E.ImageMessage{
			URL:           proto.String(message.URL),
			MediaKey:      message.MediaKey,
			FileSHA256:    message.FileSHA256,
			FileEncSHA256: message.FileEncSHA256,
			FileLength:    proto.Uint64(message.FileLength),
		}
	case "video":
		downloadableMsg = &waE2E.VideoMessage{
			URL:           proto.String(message.URL),
			MediaKey:      message.MediaKey,
			FileSHA256:    message.FileSHA256,
			FileEncSHA256: message.FileEncSHA256,
			FileLength:    proto.Uint64(message.FileLength),
		}
	case "audio":
		downloadableMsg = &waE2E.AudioMessage{
			URL:           proto.String(message.URL),
			MediaKey:      message.MediaKey,
			FileSHA256:    message.FileSHA256,
			FileEncSHA256: message.FileEncSHA256,
			FileLength:    proto.Uint64(message.FileLength),
		}
	case "document":
		downloadableMsg = &waE2E.DocumentMessage{
			URL:           proto.String(message.URL),
			MediaKey:      message.MediaKey,
			FileSHA256:    message.FileSHA256,
			FileEncSHA256: message.FileEncSHA256,
			FileLength:    proto.Uint64(message.FileLength),
			FileName:      proto.String(message.Filename),
		}
	case "sticker":
		downloadableMsg = &waE2E.StickerMessage{
			URL:           proto.String(message.URL),
			MediaKey:      message.MediaKey,
			FileSHA256:    message.FileSHA256,
			FileEncSHA256: message.FileEncSHA256,
			FileLength:    proto.Uint64(message.FileLength),
		}
	default:
		return response, fmt.Errorf("unsupported media type: %s", message.MediaType)
	}

	// Download the media using existing utils.ExtractMedia function
	extractedMedia, err := utils.ExtractMedia(ctx, client, dateDir, downloadableMsg.(whatsmeow.DownloadableMessage))
	if err != nil {
		return response, fmt.Errorf("failed to download media: %v", err)
	}

	// Get file size
	fileInfo, err := os.Stat(extractedMedia.MediaPath)
	if err != nil {
		logrus.Warnf("Could not get file size for %s: %v", extractedMedia.MediaPath, err)
	}

	// Build response
	response.MessageID = request.MessageID
	response.Status = fmt.Sprintf("Media downloaded successfully to %s", extractedMedia.MediaPath)
	response.MediaType = message.MediaType
	response.Filename = filepath.Base(extractedMedia.MediaPath)
	response.FilePath = extractedMedia.MediaPath
	if fileInfo != nil {
		response.FileSize = fileInfo.Size()
	}

	logrus.Info(map[string]any{
		"message_id": request.MessageID,
		"phone":      request.Phone,
		"chat":       dataWaRecipient.String(),
		"media_type": response.MediaType,
		"file_path":  response.FilePath,
		"file_size":  response.FileSize,
	})

	return response, nil
}

func buildForwardMessageProto(message *domainChatStorage.Message, markAsForwarded bool, thumbnail []byte) (*waE2E.Message, string, error) {
	if message == nil {
		return nil, "", fmt.Errorf("message not found")
	}

	contextInfo := forwardedContextInfo(markAsForwarded)

	switch message.MediaType {
	case "":
		if message.Content == "" {
			return nil, "", fmt.Errorf("message %s has no forwardable content", message.ID)
		}
		return &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String(message.Content),
				ContextInfo: contextInfo,
			},
		}, message.Content, nil
	case "image":
		if err := validateForwardableMedia(message); err != nil {
			return nil, "", err
		}
		return &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Caption:       proto.String(message.Content),
				URL:           proto.String(message.URL),
				Mimetype:      proto.String("image/jpeg"),
				MediaKey:      message.MediaKey,
				FileSHA256:    message.FileSHA256,
				FileEncSHA256: message.FileEncSHA256,
				FileLength:    proto.Uint64(message.FileLength),
				JPEGThumbnail: thumbnail,
				ContextInfo:   contextInfo,
			},
		}, message.Content, nil
	case "video", "video_note":
		if err := validateForwardableMedia(message); err != nil {
			return nil, "", err
		}
		caption := forwardVideoCaption(message)
		return &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				Caption:       proto.String(caption),
				URL:           proto.String(message.URL),
				Mimetype:      proto.String("video/mp4"),
				MediaKey:      message.MediaKey,
				FileSHA256:    message.FileSHA256,
				FileEncSHA256: message.FileEncSHA256,
				FileLength:    proto.Uint64(message.FileLength),
				JPEGThumbnail: thumbnail,
				ContextInfo:   contextInfo,
			},
		}, caption, nil
	case "audio":
		if err := validateForwardableMedia(message); err != nil {
			return nil, "", err
		}
		return &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				URL:           proto.String(message.URL),
				Mimetype:      proto.String("audio/ogg; codecs=opus"),
				MediaKey:      message.MediaKey,
				FileSHA256:    message.FileSHA256,
				FileEncSHA256: message.FileEncSHA256,
				FileLength:    proto.Uint64(message.FileLength),
				ContextInfo:   contextInfo,
			},
		}, message.Content, nil
	case "document":
		if err := validateForwardableMedia(message); err != nil {
			return nil, "", err
		}
		return &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				Caption:       proto.String(message.Content),
				URL:           proto.String(message.URL),
				Mimetype:      proto.String("application/octet-stream"),
				FileName:      proto.String(message.Filename),
				MediaKey:      message.MediaKey,
				FileSHA256:    message.FileSHA256,
				FileEncSHA256: message.FileEncSHA256,
				FileLength:    proto.Uint64(message.FileLength),
				ContextInfo:   contextInfo,
			},
		}, message.Content, nil
	case "sticker":
		if err := validateForwardableMedia(message); err != nil {
			return nil, "", err
		}
		return &waE2E.Message{
			StickerMessage: &waE2E.StickerMessage{
				URL:           proto.String(message.URL),
				Mimetype:      proto.String("image/webp"),
				MediaKey:      message.MediaKey,
				FileSHA256:    message.FileSHA256,
				FileEncSHA256: message.FileEncSHA256,
				FileLength:    proto.Uint64(message.FileLength),
				ContextInfo:   contextInfo,
			},
		}, message.Content, nil
	default:
		return nil, "", fmt.Errorf("unsupported media type for forwarding: %s", message.MediaType)
	}
}

func forwardedContextInfo(markAsForwarded bool) *waE2E.ContextInfo {
	if !markAsForwarded {
		return nil
	}

	return &waE2E.ContextInfo{
		IsForwarded:     proto.Bool(true),
		ForwardingScore: proto.Uint32(100),
	}
}

func forwardVideoCaption(message *domainChatStorage.Message) string {
	if message == nil {
		return ""
	}

	caption := message.Content
	if !message.IsFromMe {
		return caption
	}
	if caption == "🎥 Video" {
		return ""
	}
	return strings.TrimPrefix(caption, "🎥 ")
}

func generateForwardPreviewThumbnail(ctx context.Context, client *whatsmeow.Client, message *domainChatStorage.Message) []byte {
	if client == nil || message == nil {
		return nil
	}

	switch message.MediaType {
	case "image":
		downloadable := &waE2E.ImageMessage{
			URL:           proto.String(message.URL),
			Mimetype:      proto.String("image/jpeg"),
			MediaKey:      message.MediaKey,
			FileSHA256:    message.FileSHA256,
			FileEncSHA256: message.FileEncSHA256,
			FileLength:    proto.Uint64(message.FileLength),
		}
		data, err := client.Download(ctx, downloadable)
		if err != nil {
			logrus.Warnf("Failed to download image %s for forward preview: %v", message.ID, err)
			return nil
		}
		thumbnail, err := imageBytesToJPEGThumbnail(data)
		if err != nil {
			logrus.Warnf("Failed to generate image preview for forwarded message %s: %v", message.ID, err)
			return nil
		}
		return thumbnail
	case "video", "video_note":
		downloadable := &waE2E.VideoMessage{
			URL:           proto.String(message.URL),
			Mimetype:      proto.String("video/mp4"),
			MediaKey:      message.MediaKey,
			FileSHA256:    message.FileSHA256,
			FileEncSHA256: message.FileEncSHA256,
			FileLength:    proto.Uint64(message.FileLength),
		}
		data, err := client.Download(ctx, downloadable)
		if err != nil {
			logrus.Warnf("Failed to download video %s for forward preview: %v", message.ID, err)
			return nil
		}
		thumbnail, err := videoBytesToJPEGThumbnail(ctx, data)
		if err != nil {
			logrus.Warnf("Failed to generate video preview for forwarded message %s: %v", message.ID, err)
			return nil
		}
		return thumbnail
	default:
		return nil
	}
}

func imageBytesToJPEGThumbnail(data []byte) ([]byte, error) {
	srcImage, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return encodeForwardThumbnail(srcImage)
}

func videoBytesToJPEGThumbnail(ctx context.Context, data []byte) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not installed: %w", err)
	}
	if err := os.MkdirAll(config.PathSendItems, 0755); err != nil {
		return nil, err
	}

	inputFile, err := os.CreateTemp(config.PathSendItems, "forward-preview-*.mp4")
	if err != nil {
		return nil, err
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)
	if _, err := inputFile.Write(data); err != nil {
		inputFile.Close()
		return nil, err
	}
	if err := inputFile.Close(); err != nil {
		return nil, err
	}

	outputFile, err := os.CreateTemp(config.PathSendItems, "forward-preview-*.jpg")
	if err != nil {
		return nil, err
	}
	outputPath := outputFile.Name()
	outputFile.Close()
	defer os.Remove(outputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-ss", "00:00:01.000", "-i", inputPath, "-frames:v", "1", outputPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg thumbnail failed: %w: %s", err, string(output))
	}

	srcImage, err := imaging.Open(outputPath)
	if err != nil {
		return nil, err
	}
	return encodeForwardThumbnail(srcImage)
}

func encodeForwardThumbnail(srcImage image.Image) ([]byte, error) {
	resizedImage := imaging.Resize(srcImage, 100, 0, imaging.Lanczos)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resizedImage, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sendForwardMessageWithRetry(ctx context.Context, client *whatsmeow.Client, recipient types.JID, msg *waE2E.Message) (whatsmeow.SendResponse, error) {
	response, err := client.SendMessage(ctx, recipient, msg)
	if err == nil {
		return response, nil
	}
	if !strings.Contains(err.Error(), "server returned error 479") {
		return response, err
	}

	logrus.Warnf("Forward send got WhatsApp server error 479; retrying once | chat=%s", recipient.String())
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return response, err
	case <-timer.C:
	}

	retryResponse, retryErr := client.SendMessage(ctx, recipient, msg)
	if retryErr != nil {
		return retryResponse, fmt.Errorf("%w (first attempt: %v)", retryErr, err)
	}
	return retryResponse, nil
}

func validateForwardableMedia(message *domainChatStorage.Message) error {
	if message.URL == "" || len(message.MediaKey) == 0 || len(message.FileSHA256) == 0 || len(message.FileEncSHA256) == 0 {
		return fmt.Errorf("message %s is missing stored media data and cannot be forwarded", message.ID)
	}
	return nil
}

func (service serviceMessage) storeForwardedMessage(ctx context.Context, client *whatsmeow.Client, recipient types.JID, messageID string, content string, timestamp time.Time, msg *waE2E.Message) {
	storeBaseCtx := context.Background()
	if device, ok := whatsapp.DeviceFromContext(ctx); ok {
		storeBaseCtx = whatsapp.ContextWithDevice(storeBaseCtx, device)
	}

	storeCtx, cancel := context.WithTimeout(storeBaseCtx, 2*time.Second)
	defer cancel()

	senderJID := ""
	if client.Store.ID != nil {
		senderJID = client.Store.ID.String()
	}

	if err := service.chatStorageRepo.StoreSentMessageWithContext(storeCtx, messageID, senderJID, recipient.String(), content, timestamp, msg); err != nil {
		logrus.Warnf("Failed to store forwarded message: %v", err)
	}
}
