package bing

import (
	"errors"
	"fmt"
	"github.com/bincooo/chatgpt-adapter/v2/internal/middle"
	"github.com/bincooo/chatgpt-adapter/v2/pkg/gpt"
	"github.com/bincooo/edge-api"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"strings"
	"time"
)

const MODEL = "bing"

func Complete(ctx *gin.Context, cookie, proxies string, chatCompletionRequest gpt.ChatCompletionRequest) {
	options, err := edge.NewDefaultOptions(cookie, "")
	if err != nil {
		middle.ResponseWithE(ctx, err)
		return
	}

	messages := chatCompletionRequest.Messages
	messageL := len(messages)
	if messageL == 0 {
		middle.ResponseWithV(ctx, "[] is too short - 'messages'")
		return
	}

	if messages[messageL-1]["role"] != "function" && len(chatCompletionRequest.Tools) > 0 {
		goOn, _err := completeToolCalls(ctx, cookie, proxies, chatCompletionRequest)
		if _err != nil {
			middle.ResponseWithE(ctx, _err)
			return
		}
		if !goOn {
			return
		}
	}

	pMessages, prompt, err := buildConversation(messages)
	if err != nil {
		middle.ResponseWithE(ctx, err)
		return
	}

	chat := edge.New(options.
		Proxies(proxies).
		TopicToE(true).
		Model(edge.ModelSydney).
		Temperature(chatCompletionRequest.Temperature))

	chatResponse, err := chat.Reply(ctx.Request.Context(), prompt, nil, pMessages)
	if err != nil {
		middle.ResponseWithE(ctx, err)
		return
	}
	waitResponse(ctx, chatResponse, chatCompletionRequest.Stream)
}

func completeToolCalls(ctx *gin.Context, cookie, proxies string, chatCompletionRequest gpt.ChatCompletionRequest) (bool, error) {
	toolsMap, prompt, err := buildToolsPrompt(chatCompletionRequest.Tools, chatCompletionRequest.Messages)
	if err != nil {
		return false, err
	}

	options, err := edge.NewDefaultOptions(cookie, "")
	if err != nil {
		return false, err
	}

	chat := edge.New(options.
		Proxies(proxies).
		TopicToE(true).
		Notebook(true).
		Model(edge.ModelCreative).
		Temperature(chatCompletionRequest.Temperature))
	chatResponse, err := chat.Reply(ctx.Request.Context(), prompt, nil, nil)
	if err != nil {
		return false, err
	}

	content, err := waitMessage(chatResponse)
	if err != nil {
		return false, err
	}
	logrus.Infof("completeTools response: \n%s", content)
	return parseToToolCall(ctx, toolsMap, content, chatCompletionRequest.Stream)
}

func parseToToolCall(ctx *gin.Context, toolsMap map[string]string, content string, sse bool) (bool, error) {
	created := time.Now().Unix()
	for k, v := range toolsMap {
		if strings.Contains(content, k) {
			left := strings.Index(content, "{")
			right := strings.LastIndex(content, "}")
			argv := ""
			if left >= 0 && right > left {
				argv = content[left : right+1]
			}

			if sse {
				middle.ResponseWithSSEToolCalls(ctx, MODEL, v, argv, created)
				return false, nil
			} else {
				middle.ResponseWithToolCalls(ctx, MODEL, v, argv)
				return false, nil
			}
		}
	}
	return true, nil
}

func waitMessage(chatResponse chan edge.ChatResponse) (content string, err error) {

	for {
		message, ok := <-chatResponse
		if !ok {
			break
		}

		if message.Error != nil {
			return "", message.Error.Message
		}

		if len(message.Text) > 0 {
			content = message.Text
		}
	}

	return content, nil
}

func waitResponse(ctx *gin.Context, chatResponse chan edge.ChatResponse, sse bool) {
	pos := 0
	content := ""
	created := time.Now().Unix()
	logrus.Infof("waitResponse ...")

	for {
		message, ok := <-chatResponse
		if !ok {
			break
		}

		if message.Error != nil {
			middle.ResponseWithE(ctx, message.Error)
			return
		}

		if sse {
			contentL := len(message.Text)
			if pos < contentL {
				value := message.Text[pos:contentL]
				fmt.Printf("----- raw -----\n %s\n", value)
				middle.ResponseWithSSE(ctx, MODEL, value, created)
			}
			pos = contentL
		} else if len(message.Text) > 0 {
			content = message.Text
		}
	}

	if !sse {
		fmt.Printf("----- raw -----\n %s\n", content)
		middle.ResponseWith(ctx, MODEL, content)
	} else {
		middle.ResponseWithSSE(ctx, MODEL, "[DONE]", created)
	}
}

func buildConversation(messages []map[string]string) (pMessages []edge.ChatMessage, prompt string, err error) {
	pos := len(messages) - 1
	if pos < 0 {
		return
	}
	if messages[pos]["role"] == "user" {
		prompt = messages[pos]["content"]
		messages = messages[:pos]
	} else {
		prompt = "continue"
	}

	pos = 0
	messageL := len(messages)

	role := ""
	buffer := make([]string, 0)

	toA := func(expr string) string {
		switch expr {
		case "system", "user", "function":
			return "user"
		case "assistant":
			return "bot"
		default:
			return ""
		}
	}

	for {
		if pos >= messageL {
			if len(buffer) > 0 {
				pMessages = append(pMessages, edge.ChatMessage{
					"author": role,
					"text":   strings.Join(buffer, "\n\n"),
				})
			}
			break
		}

		message := messages[pos]
		curr := toA(message["role"])
		content := message["content"]
		if curr == "" {
			return nil, "", errors.New(
				fmt.Sprintf("'%s' is not one of ['system', 'assistant', 'user', 'function'] - 'messages.%d.role'",
					message["role"], pos))
		}
		pos++
		if role == "" {
			role = curr
		}

		if curr == role {
			if message["role"] == "function" {
				content = fmt.Sprintf("这是系统内置tools工具的返回结果: (%s)\n\n##\n%s\n##\n---\n\n%s", message["name"], content, prompt)
			}
			buffer = append(buffer, content)
			continue
		}
		pMessages = append(pMessages, edge.ChatMessage{
			"author": role,
			"text":   strings.Join(buffer, "\n\n"),
		})
		buffer = append(make([]string, 0), content)
		role = curr
	}

	return pMessages, prompt, nil
}
