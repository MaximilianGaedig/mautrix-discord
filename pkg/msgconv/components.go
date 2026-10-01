// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package msgconv

import (
	"html"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// maxListedSelectOptions bounds how many options of a select menu are listed.
// Discord allows 25 per menu, which would bury the message under its menu.
const maxListedSelectOptions = 10

// componentFallback collects what can be shown of a message's components
// without being able to interact with them.
type componentFallback struct {
	// texts are the rendered text blocks of a components-v2 message, which
	// carries its text in components instead of in the message content.
	texts []string
	// rows are the lines of controls, one per action row.
	rows []string

	hasButtons bool
	// The following say which kinds of controls only work in the Discord
	// app, which is what the closing line tells the user.
	appButtons bool
	appMenus   bool
	// other is set when a component can't be shown at all.
	other bool
}

// renderComponentsHTML renders the components of a message as HTML parts: the
// text blocks first, then one paragraph listing the buttons and menus the way
// the Telegram bridge lists inline keyboards (a row per line, " | " between
// buttons, link buttons as links), closed by a line that names the controls
// which need the Discord app. Nothing here can be clicked: that would need
// Discord's interactions endpoint.
//
// renderText converts the Discord markdown of a text block to HTML.
func renderComponentsHTML(components []discordgo.MessageComponent, renderText func(string) string) []string {
	var fb componentFallback
	fb.add(components, renderText)

	parts := fb.texts
	lines := fb.rows
	if fb.hasButtons {
		lines = append([]string{"Buttons:"}, lines...)
	}
	if note := fb.appNote(); note != "" {
		lines = append(lines, note)
	}
	if len(lines) > 0 {
		parts = append(parts, "<p>"+strings.Join(lines, "<br>")+"</p>")
	}
	return parts
}

// appNote is the closing line. It is empty when everything shown works from
// Matrix, which is the case for a message that only has link buttons.
func (fb *componentFallback) appNote() string {
	var note string
	switch {
	case fb.appButtons && fb.appMenus:
		note = "Use the Discord app to press the buttons and to pick from the menus."
	case fb.appButtons:
		note = "Use the Discord app to press the buttons."
	case fb.appMenus:
		note = "Use the Discord app to pick from the menus."
	}
	if fb.other {
		if note != "" {
			note += " "
		}
		note += "Some parts of this message are only shown in the Discord app."
	}
	return note
}

func (fb *componentFallback) add(components []discordgo.MessageComponent, renderText func(string) string) {
	for _, component := range components {
		// discordgo decodes received components into pointers, but the value
		// types implement the interface too.
		switch c := component.(type) {
		case *discordgo.ActionsRow:
			fb.addRow(c.Components)
		case discordgo.ActionsRow:
			fb.addRow(c.Components)
		case *discordgo.Container:
			fb.add(c.Components, renderText)
		case discordgo.Container:
			fb.add(c.Components, renderText)
		case *discordgo.Section:
			fb.addSection(c, renderText)
		case discordgo.Section:
			fb.addSection(&c, renderText)
		case *discordgo.TextDisplay:
			fb.addText(c.Content, renderText)
		case discordgo.TextDisplay:
			fb.addText(c.Content, renderText)
		case *discordgo.Separator, discordgo.Separator:
			// Only spacing, nothing to show.
		case nil:
		default:
			// A button or a menu outside an action row doesn't happen in
			// messages from Discord, but showing it is still better than not.
			if control := fb.renderControl(component); control != "" {
				fb.rows = append(fb.rows, control)
			} else {
				fb.other = true
			}
		}
	}
}

func (fb *componentFallback) addText(content string, renderText func(string) string) {
	if strings.TrimSpace(content) != "" {
		fb.texts = append(fb.texts, renderText(content))
	}
}

// addSection adds a section: up to three text blocks with a button or a
// thumbnail next to them.
func (fb *componentFallback) addSection(section *discordgo.Section, renderText func(string) string) {
	fb.add(section.Components, renderText)
	if section.Accessory == nil {
		return
	}
	if control := fb.renderControl(section.Accessory); control != "" {
		fb.rows = append(fb.rows, control)
	} else {
		fb.other = true
	}
}

func (fb *componentFallback) addRow(components []discordgo.MessageComponent) {
	controls := make([]string, 0, len(components))
	for _, component := range components {
		if control := fb.renderControl(component); control != "" {
			controls = append(controls, control)
		} else if component != nil {
			fb.other = true
		}
	}
	if len(controls) > 0 {
		fb.rows = append(fb.rows, strings.Join(controls, " | "))
	}
}

// renderControl renders a button or a select menu, and returns an empty
// string for anything else.
func (fb *componentFallback) renderControl(component discordgo.MessageComponent) string {
	switch c := component.(type) {
	case *discordgo.Button:
		return fb.renderButton(c)
	case discordgo.Button:
		return fb.renderButton(&c)
	case *discordgo.SelectMenu:
		return fb.renderSelectMenu(c)
	case discordgo.SelectMenu:
		return fb.renderSelectMenu(&c)
	}
	return ""
}

func (fb *componentFallback) renderButton(button *discordgo.Button) string {
	fb.hasButtons = true
	label := componentLabel(button.Emoji, button.Label)
	switch {
	case button.Style == discordgo.PremiumButton && label == "":
		// Premium buttons have no label of their own: Discord shows the
		// name and the price of the product they sell.
		label = "(purchase)"
	case label == "":
		label = "(no label)"
	}
	escaped := html.EscapeString(label)
	switch {
	case button.URL != "":
		// A link button is only a URL, so it works without interactions.
		return `<a href="` + html.EscapeString(button.URL) + `">` + escaped + `</a>`
	case button.Disabled:
		return "<del>" + escaped + "</del>"
	}
	fb.appButtons = true
	return escaped
}

func (fb *componentFallback) renderSelectMenu(menu *discordgo.SelectMenu) string {
	var sb strings.Builder
	sb.WriteString("Menu")
	if placeholder := compactComponentText(menu.Placeholder); placeholder != "" {
		sb.WriteString(" “" + html.EscapeString(placeholder) + "”")
	}
	if len(menu.Options) > 0 {
		sb.WriteString(": ")
		for i, option := range menu.Options {
			if i == maxListedSelectOptions {
				sb.WriteString(" | …")
				break
			}
			if i > 0 {
				sb.WriteString(" | ")
			}
			label := componentLabel(option.Emoji, option.Label)
			if label == "" {
				label = compactComponentText(option.Value)
			}
			escaped := html.EscapeString(label)
			if option.Default {
				// The default option is the one currently chosen.
				escaped = "<strong>" + escaped + "</strong>"
			}
			sb.WriteString(escaped)
		}
	} else if kind := selectMenuKind(menu.MenuType); kind != "" {
		// These menus are filled in by the Discord client from the server's
		// own users, roles or channels, so there are no options to list.
		sb.WriteString(" (" + kind + ")")
	}
	if menu.Disabled {
		return "<del>" + sb.String() + "</del>"
	}
	fb.appMenus = true
	return sb.String()
}

func selectMenuKind(menuType discordgo.SelectMenuType) string {
	switch menuType {
	case discordgo.UserSelectMenu:
		return "pick a user"
	case discordgo.RoleSelectMenu:
		return "pick a role"
	case discordgo.MentionableSelectMenu:
		return "pick a user or a role"
	case discordgo.ChannelSelectMenu:
		return "pick a channel"
	}
	return ""
}

// componentLabel is the label of a button or an option with its emoji in
// front. A custom emoji is shown by name, like Discord does when the image
// can't be loaded.
func componentLabel(emoji *discordgo.ComponentEmoji, label string) string {
	label = compactComponentText(label)
	if emoji == nil || emoji.Name == "" {
		return label
	}
	name := emoji.Name
	if emoji.ID != "" {
		name = ":" + name + ":"
	}
	if label == "" {
		return name
	}
	return name + " " + label
}

// compactComponentText keeps a label on the one line it is listed on.
func compactComponentText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
