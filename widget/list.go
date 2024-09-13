package widget

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/internal/cache"
	"fyne.io/fyne/v2/internal/widget"
	"fyne.io/fyne/v2/theme"
)

// ListItemID uniquely identifies an item within a list.
type ListItemID = int

// Declare conformity with interfaces.
var _ fyne.Widget = (*List)(nil)
var _ fyne.Focusable = (*List)(nil)

// List is a widget that pools list items for performance and
// lays the items out in a vertical direction inside of a scroller.
// By default, List requires that all items are the same size, but specific
// rows can have their heights set with SetItemHeight.
//
// Since: 1.4
type List struct {
	BaseWidget

	Length       func() int                                  `json:"-"`
	CreateItem   func() fyne.CanvasObject                    `json:"-"`
	UpdateItem   func(id ListItemID, item fyne.CanvasObject) `json:"-"`
	OnSelected   func(id ListItemID)                         `json:"-"`
	OnUnselected func(id ListItemID)                         `json:"-"`

	// HideSeparators hides the separators between list rows
	//
	// Since: 2.5
	HideSeparators bool

	currentFocus  ListItemID
	focused       bool
	scroller      *widget.Scroll
	selected      []ListItemID
	itemMin       fyne.Size
	itemMeasures  map[ListItemID]float32
	offset        float32
	offsetUpdated func(fyne.Position)
	orientation   Orientation
}

// NewList creates and returns a list widget for displaying items in
// a vertical layout with scrolling and caching for performance.
//
// Since: 1.4
func NewList(length func() int, createItem func() fyne.CanvasObject, updateItem func(ListItemID, fyne.CanvasObject)) *List {
	list := &List{Length: length, CreateItem: createItem, UpdateItem: updateItem, orientation: Vertical}
	list.ExtendBaseWidget(list)
	return list
}

// NewListWithData creates a new list widget that will display the contents of the provided data.
//
// Since: 2.0
func NewListWithData(data binding.DataList, createItem func() fyne.CanvasObject, updateItem func(binding.DataItem, fyne.CanvasObject)) *List {
	l := NewList(
		data.Length,
		createItem,
		func(i ListItemID, o fyne.CanvasObject) {
			item, err := data.GetItem(i)
			if err != nil {
				fyne.LogError(fmt.Sprintf("Error getting data item %d", i), err)
				return
			}
			updateItem(item, o)
		})

	data.AddListener(binding.NewDataListener(l.Refresh))
	return l
}

func NewHorizontalList(length func() int, createItem func() fyne.CanvasObject, updateItem func(ListItemID, fyne.CanvasObject)) *List {
	list := NewList(length, createItem, updateItem)
	list.orientation = Horizontal
	return list
}

func NewHorizontalListWithData(data binding.DataList, createItem func() fyne.CanvasObject, updateItem func(binding.DataItem, fyne.CanvasObject)) *List {
	l := NewListWithData(data, createItem, updateItem)
	l.orientation = Horizontal
	return l
}

// CreateRenderer is a private method to Fyne which links this widget to its renderer.
func (l *List) CreateRenderer() fyne.WidgetRenderer {
	l.ExtendBaseWidget(l)

	if f := l.CreateItem; f != nil && l.itemMin.IsZero() {
		item := createItemAndApplyThemeScope(f, l)
		l.itemMin = item.MinSize()
	}

	layout := &fyne.Container{Layout: newListLayout(l)}
	if l.orientation == Horizontal {
		l.scroller = widget.NewHScroll(layout)
	} else {
		l.scroller = widget.NewVScroll(layout)
	}
	layout.Resize(layout.MinSize())
	objects := []fyne.CanvasObject{l.scroller}
	return newListRenderer(objects, l, l.scroller, layout)
}

// FocusGained is called after this List has gained focus.
//
// Implements: fyne.Focusable
func (l *List) FocusGained() {
	l.focused = true
	l.scrollTo(l.currentFocus)
	l.RefreshItem(l.currentFocus)
}

// FocusLost is called after this List has lost focus.
//
// Implements: fyne.Focusable
func (l *List) FocusLost() {
	l.focused = false
	l.RefreshItem(l.currentFocus)
}

// MinSize returns the size that this widget should not shrink below.
func (l *List) MinSize() fyne.Size {
	l.ExtendBaseWidget(l)
	return l.BaseWidget.MinSize()
}

// RefreshItem refreshes a single item, specified by the item ID passed in.
//
// Since: 2.4
func (l *List) RefreshItem(id ListItemID) {
	if l.scroller == nil {
		return
	}
	l.BaseWidget.Refresh()
	lo := l.scroller.Content.(*fyne.Container).Layout.(*listLayout)
	lo.renderLock.RLock() // ensures we are not changing visible info in render code during the search
	item, ok := lo.searchVisible(lo.visible, id)
	lo.renderLock.RUnlock()
	if ok {
		lo.setupListItem(item, id, l.focused && l.currentFocus == id)
	}
}

// SetItemHeight supports changing the height of the specified list item. Items normally take the height of the template
// returned from the CreateItem callback. The height parameter uses the same units as a fyne.Size type and refers
// to the internal content height not including the divider size.
//
// Deprecated: Use SetItemMeasure instead
// Since: 2.3
func (l *List) SetItemHeight(id ListItemID, height float32) {
	l.SetItemMeasure(id, height)
}

func (l *List) SetItemMeasure(id ListItemID, measure float32) {
	l.propertyLock.Lock()

	if l.itemMeasures == nil {
		l.itemMeasures = make(map[ListItemID]float32)
	}

	refresh := l.itemMeasures[id] != measure
	l.itemMeasures[id] = measure
	l.propertyLock.Unlock()

	if refresh {
		l.RefreshItem(id)
	}
}

func (l *List) scrollTo(id ListItemID) {
	if l.scroller == nil {
		return
	}

	separatorThickness := l.Theme().Size(theme.SizeNamePadding)
	axis := float32(0)
	lastItemMeasure := float32(0)
	if l.orientation == Horizontal {
		lastItemMeasure = l.itemMin.Width
		if l.itemMeasures == nil || len(l.itemMeasures) == 0 {
			axis = (float32(id) * l.itemMin.Width) + (float32(id) * separatorThickness)
		} else {
			for i := 0; i < id; i++ {
				width := l.itemMin.Width
				if w, ok := l.itemMeasures[i]; ok {
					width = w
				}

				axis += width + separatorThickness
				lastItemMeasure = width
			}
		}
		if axis < l.scroller.Offset.X {
			l.scroller.Offset.X = axis
		} else if axis+l.itemMin.Width > l.scroller.Offset.X+l.scroller.Size().Width {
			l.scroller.Offset.X = axis + lastItemMeasure - l.scroller.Size().Width
		}
	} else {
		lastItemMeasure = l.itemMin.Height
		if l.itemMeasures == nil || len(l.itemMeasures) == 0 {
			axis = (float32(id) * l.itemMin.Height) + (float32(id) * separatorThickness)
		} else {
			for i := 0; i < id; i++ {
				height := l.itemMin.Height
				if h, ok := l.itemMeasures[i]; ok {
					height = h
				}

				axis += height + separatorThickness
				lastItemMeasure = height
			}
		}

		if axis < l.scroller.Offset.Y {
			l.scroller.Offset.Y = axis
		} else if axis+l.itemMin.Height > l.scroller.Offset.Y+l.scroller.Size().Height {
			l.scroller.Offset.Y = axis + lastItemMeasure - l.scroller.Size().Height
		}
	}
	l.offsetUpdated(l.scroller.Offset)
}

// Resize is called when this list should change size. We refresh to ensure invisible items are drawn.
func (l *List) Resize(s fyne.Size) {
	l.BaseWidget.Resize(s)
	if l.scroller == nil {
		return
	}

	l.offsetUpdated(l.scroller.Offset)
	l.scroller.Content.(*fyne.Container).Layout.(*listLayout).updateList(true)
}

// Select add the item identified by the given ID to the selection.
func (l *List) Select(id ListItemID) {
	if len(l.selected) > 0 && id == l.selected[0] {
		return
	}
	length := 0
	if f := l.Length; f != nil {
		length = f()
	}
	if id < 0 || id >= length {
		return
	}
	old := l.selected
	l.selected = []ListItemID{id}
	defer func() {
		if f := l.OnUnselected; f != nil && len(old) > 0 {
			f(old[0])
		}
		if f := l.OnSelected; f != nil {
			f(id)
		}
	}()
	l.scrollTo(id)
	l.Refresh()
}

// ScrollTo scrolls to the item represented by id
//
// Since: 2.1
func (l *List) ScrollTo(id ListItemID) {
	length := 0
	if f := l.Length; f != nil {
		length = f()
	}
	if id < 0 || id >= length {
		return
	}
	l.scrollTo(id)
	l.Refresh()
}

// ScrollToBottom scrolls to the end of the list
// Deprecated: Use ScrollToEnd instead
// Since: 2.1
func (l *List) ScrollToBottom() {
	l.ScrollToEnd()
}

// ScrollToTop scrolls to the start of the list
//
// Deprecated: Use ScrollToStart instead
// Since: 2.1
func (l *List) ScrollToTop() {
	l.ScrollToStart()
}

// ScrollToEnd scrolls to the end of the list
//
// Since: 2.1
func (l *List) ScrollToEnd() {
	length := 0
	if f := l.Length; f != nil {
		length = f()
	}
	if length > 0 {
		length--
	}
	l.scrollTo(length)
	l.Refresh()
}

// ScrollToStart scrolls to the beginning of the list
//
// Since: 2.1
func (l *List) ScrollToStart() {
	l.scrollTo(0)
	l.Refresh()
}

// ScrollToOffset scrolls the list to the given offset position.
//
// Since: 2.5
func (l *List) ScrollToOffset(offset float32) {
	if l.scroller == nil {
		return
	}
	if offset < 0 {
		offset = 0
	}
	var contentMeasure, viewSizeLimit float32
	if l.orientation == Horizontal {
		contentMeasure = l.contentMinSize().Width
		viewSizeLimit = l.Size().Width
	} else {
		contentMeasure = l.contentMinSize().Height
		viewSizeLimit = l.Size().Height
	}
	if viewSizeLimit >= contentMeasure {
		return // content fully visible - no need to scroll
	}
	if offset > contentMeasure {
		offset = contentMeasure
	}
	if l.orientation == Horizontal {
		l.scroller.Offset.X = offset
	} else {
		l.scroller.Offset.Y = offset
	}
	l.offsetUpdated(l.scroller.Offset)
	l.Refresh()
}

// GetScrollOffset returns the current scroll offset position
//
// Since: 2.5
func (l *List) GetScrollOffset() float32 {
	return l.offset
}

// TypedKey is called if a key event happens while this List is focused.
//
// Implements: fyne.Focusable
func (l *List) TypedKey(event *fyne.KeyEvent) {
	switch event.Name {
	case fyne.KeySpace:
		l.Select(l.currentFocus)
	case fyne.KeyDown:
		if f := l.Length; f != nil && l.currentFocus >= f()-1 {
			return
		}
		l.RefreshItem(l.currentFocus)
		l.currentFocus++
		l.scrollTo(l.currentFocus)
		l.RefreshItem(l.currentFocus)
	case fyne.KeyUp:
		if l.currentFocus <= 0 {
			return
		}
		l.RefreshItem(l.currentFocus)
		l.currentFocus--
		l.scrollTo(l.currentFocus)
		l.RefreshItem(l.currentFocus)
	}
}

// TypedRune is called if a text event happens while this List is focused.
//
// Implements: fyne.Focusable
func (l *List) TypedRune(_ rune) {
	// intentionally left blank
}

// Unselect removes the item identified by the given ID from the selection.
func (l *List) Unselect(id ListItemID) {
	if len(l.selected) == 0 || l.selected[0] != id {
		return
	}

	l.selected = nil
	l.Refresh()
	if f := l.OnUnselected; f != nil {
		f(id)
	}
}

// UnselectAll removes all items from the selection.
//
// Since: 2.1
func (l *List) UnselectAll() {
	if len(l.selected) == 0 {
		return
	}

	selected := l.selected
	l.selected = nil
	l.Refresh()
	if f := l.OnUnselected; f != nil {
		for _, id := range selected {
			f(id)
		}
	}
}

func (l *List) contentMinSize() fyne.Size {
	separatorThickness := l.Theme().Size(theme.SizeNamePadding)
	l.propertyLock.Lock()
	defer l.propertyLock.Unlock()
	if l.Length == nil {
		return fyne.NewSize(0, 0)
	}
	items := l.Length()
	if l.itemMeasures == nil || len(l.itemMeasures) == 0 {
		if l.orientation == Horizontal {
			return fyne.NewSize((l.itemMin.Width+separatorThickness)*float32(items)-separatorThickness, l.itemMin.Height)
		}
		return fyne.NewSize(l.itemMin.Width, (l.itemMin.Height+separatorThickness)*float32(items)-separatorThickness)
	}
	measure := float32(0)
	totalCustom := 0
	templateMeasure := l.itemMin.Height
	if l.orientation == Horizontal {
		templateMeasure = l.itemMin.Width
	}
	for id, itemMeasure := range l.itemMeasures {
		if id < items {
			totalCustom++
			measure += itemMeasure
		}
	}
	measure += float32(items-totalCustom) * templateMeasure
	calculatedMeasure := measure + separatorThickness*float32(items-1)
	if l.orientation == Horizontal {
		return fyne.NewSize(calculatedMeasure, l.itemMin.Height)
	}
	return fyne.NewSize(l.itemMin.Width, calculatedMeasure)
}

// fills l.visibleRowHeights and also returns offY and minRow
// Drepecated: Use calculateVisibleItemMeasures instead
func (l *listLayout) calculateVisibleRowHeights(itemHeight float32, length int, th fyne.Theme) (offY float32, minRow int) {
	return l.calculateVisibleItemMeasures(itemHeight, length, th)
}
func (l *listLayout) calculateVisibleItemMeasures(itemMeasure float32, length int, th fyne.Theme) (off float32, minItem int) {
	scrollerMeasure := l.list.scroller.Size().Height
	if l.list.orientation == Horizontal {
		scrollerMeasure = l.list.scroller.Size().Width
	}
	if scrollerMeasure <= 0 {
		return
	}

	padding := th.Size(theme.SizeNamePadding)
	l.visibleItemMeasures = l.visibleItemMeasures[:0]
	if len(l.list.itemMeasures) == 0 {
		paddedItemMeasure := itemMeasure + padding
		off = float32(math.Floor(float64(l.list.offset/paddedItemMeasure))) * paddedItemMeasure
		minItem = int(math.Floor(float64(off / paddedItemMeasure)))
		maxItem := int(math.Ceil(float64((off + scrollerMeasure) / paddedItemMeasure)))
		if minItem > length-1 {
			minItem = length - 1
		}
		if minItem < 0 {
			minItem = 0
			off = 0
		}

		if maxItem > length-1 {
			maxItem = length - 1
		}

		for i := 0; i <= maxItem-minItem; i++ {
			l.visibleItemMeasures = append(l.visibleItemMeasures, itemMeasure)
		}
		return
	}

	offset := float32(0)
	isVisible := false
	for i := 0; i < length; i++ {
		measure := itemMeasure
		if m, ok := l.list.itemMeasures[i]; ok {
			measure = m
		}

		if offset <= l.list.offset-measure-padding {
			// before scroll
		} else if offset <= l.list.offset {
			minItem = i
			off = offset
			isVisible = true
		}
		if offset >= l.list.offset+scrollerMeasure {
			break
		}

		offset += measure + padding
		if isVisible {
			l.visibleItemMeasures = append(l.visibleItemMeasures, measure)
		}
	}
	return
}

// Declare conformity with WidgetRenderer interface.
var _ fyne.WidgetRenderer = (*listRenderer)(nil)

type listRenderer struct {
	widget.BaseRenderer

	list     *List
	scroller *widget.Scroll
	layout   *fyne.Container
}

func newListRenderer(objects []fyne.CanvasObject, l *List, scroller *widget.Scroll, layout *fyne.Container) *listRenderer {
	lr := &listRenderer{BaseRenderer: widget.NewBaseRenderer(objects), list: l, scroller: scroller, layout: layout}
	lr.scroller.OnScrolled = l.offsetUpdated
	return lr
}

func (l *listRenderer) Layout(size fyne.Size) {
	l.scroller.Resize(size)
}

func (l *listRenderer) MinSize() fyne.Size {
	return l.scroller.MinSize().Max(l.list.itemMin)
}

func (l *listRenderer) Refresh() {
	if f := l.list.CreateItem; f != nil {
		item := createItemAndApplyThemeScope(f, l.list)
		l.list.itemMin = item.MinSize()
	}
	l.Layout(l.list.Size())
	l.scroller.Refresh()
	layout := l.layout.Layout.(*listLayout)
	layout.updateList(false)

	for _, s := range layout.separators {
		s.Refresh()
	}
	canvas.Refresh(l.list.super())
}

// Declare conformity with interfaces.
var _ fyne.Widget = (*listItem)(nil)
var _ fyne.Tappable = (*listItem)(nil)
var _ desktop.Hoverable = (*listItem)(nil)

type listItem struct {
	BaseWidget

	onTapped          func()
	background        *canvas.Rectangle
	child             fyne.CanvasObject
	hovered, selected bool
}

func newListItem(child fyne.CanvasObject, tapped func()) *listItem {
	li := &listItem{
		child:    child,
		onTapped: tapped,
	}

	li.ExtendBaseWidget(li)
	return li
}

// CreateRenderer is a private method to Fyne which links this widget to its renderer.
func (li *listItem) CreateRenderer() fyne.WidgetRenderer {
	li.ExtendBaseWidget(li)
	th := li.Theme()
	v := fyne.CurrentApp().Settings().ThemeVariant()

	li.background = canvas.NewRectangle(th.Color(theme.ColorNameHover, v))
	li.background.CornerRadius = th.Size(theme.SizeNameSelectionRadius)
	li.background.Hide()

	objects := []fyne.CanvasObject{li.background, li.child}

	return &listItemRenderer{widget.NewBaseRenderer(objects), li}
}

// MinSize returns the size that this widget should not shrink below.
func (li *listItem) MinSize() fyne.Size {
	li.ExtendBaseWidget(li)
	return li.BaseWidget.MinSize()
}

// MouseIn is called when a desktop pointer enters the widget.
func (li *listItem) MouseIn(*desktop.MouseEvent) {
	li.hovered = true
	li.Refresh()
}

// MouseMoved is called when a desktop pointer hovers over the widget.
func (li *listItem) MouseMoved(*desktop.MouseEvent) {
}

// MouseOut is called when a desktop pointer exits the widget.
func (li *listItem) MouseOut() {
	li.hovered = false
	li.Refresh()
}

// Tapped is called when a pointer tapped event is captured and triggers any tap handler.
func (li *listItem) Tapped(*fyne.PointEvent) {
	if li.onTapped != nil {
		li.selected = true
		li.Refresh()
		li.onTapped()
	}
}

// Declare conformity with the WidgetRenderer interface.
var _ fyne.WidgetRenderer = (*listItemRenderer)(nil)

type listItemRenderer struct {
	widget.BaseRenderer

	item *listItem
}

// MinSize calculates the minimum size of a listItem.
// This is based on the size of the status indicator and the size of the child object.
func (li *listItemRenderer) MinSize() fyne.Size {
	return li.item.child.MinSize()
}

// Layout the components of the listItem widget.
func (li *listItemRenderer) Layout(size fyne.Size) {
	li.item.background.Resize(size)
	li.item.child.Resize(size)
}

func (li *listItemRenderer) Refresh() {
	th := li.item.Theme()
	v := fyne.CurrentApp().Settings().ThemeVariant()

	li.item.background.CornerRadius = th.Size(theme.SizeNameSelectionRadius)
	if li.item.selected {
		li.item.background.FillColor = th.Color(theme.ColorNameSelection, v)
		li.item.background.Show()
	} else if li.item.hovered {
		li.item.background.FillColor = th.Color(theme.ColorNameHover, v)
		li.item.background.Show()
	} else {
		li.item.background.Hide()
	}
	li.item.background.Refresh()
	canvas.Refresh(li.item.super())
}

// Declare conformity with Layout interface.
var _ fyne.Layout = (*listLayout)(nil)

type listItemAndID struct {
	item *listItem
	id   ListItemID
}

type listLayout struct {
	list       *List
	separators []fyne.CanvasObject
	children   []fyne.CanvasObject

	itemPool            syncPool
	visible             []listItemAndID
	slicePool           sync.Pool // *[]itemAndID
	visibleItemMeasures []float32
	renderLock          sync.RWMutex
}

func newListLayout(list *List) fyne.Layout {
	l := &listLayout{list: list}
	l.slicePool.New = func() any {
		s := make([]listItemAndID, 0)
		return &s
	}
	list.offsetUpdated = l.offsetUpdated
	return l
}

func (l *listLayout) Layout([]fyne.CanvasObject, fyne.Size) {
	l.updateList(true)
}

func (l *listLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return l.list.contentMinSize()
}

func (l *listLayout) getItem() *listItem {
	item := l.itemPool.Obtain()
	if item == nil {
		if f := l.list.CreateItem; f != nil {
			item2 := createItemAndApplyThemeScope(f, l.list)

			item = newListItem(item2, nil)
		}
	}
	return item.(*listItem)
}

func (l *listLayout) offsetUpdated(pos fyne.Position) {
	offset := pos.Y
	if l.list.orientation == Horizontal {
		offset = pos.X
	}

	if l.list.offset == offset {
		return
	}

	l.list.offset = offset
	l.updateList(true)
}

func (l *listLayout) setupListItem(li *listItem, id ListItemID, focus bool) {
	previousIndicator := li.selected
	li.selected = false
	for _, s := range l.list.selected {
		if id == s {
			li.selected = true
			break
		}
	}
	if focus {
		li.hovered = true
		li.Refresh()
	} else if previousIndicator != li.selected || li.hovered {
		li.hovered = false
		li.Refresh()
	}
	if f := l.list.UpdateItem; f != nil {
		f(id, li.child)
	}
	li.onTapped = func() {
		if !fyne.CurrentDevice().IsMobile() {
			canvas := fyne.CurrentApp().Driver().CanvasForObject(l.list)
			if canvas != nil {
				canvas.Focus(l.list)
			}

			l.list.currentFocus = id
		}

		l.list.Select(id)
	}
}

func (l *listLayout) updateList(newOnly bool) {
	th := l.list.Theme()
	separatorThickness := th.Size(theme.SizeNamePadding)
	l.renderLock.Lock()
	measure := l.list.Size().Width
	length := 0
	if f := l.list.Length; f != nil {
		length = f()
	}
	if l.list.UpdateItem == nil {
		fyne.LogError("Missing UpdateCell callback required for List", nil)
	}

	// Keep pointer reference for copying slice header when returning to the pool
	// https://blog.mike.norgate.xyz/unlocking-go-slice-performance-navigating-sync-pool-for-enhanced-efficiency-7cb63b0b453e
	wasVisiblePtr := l.slicePool.Get().(*[]listItemAndID)
	wasVisible := (*wasVisiblePtr)[:0]
	wasVisible = append(wasVisible, l.visible...)
	l.list.propertyLock.Lock()
	minItemMeasure := l.list.itemMin.Height

	if l.list.orientation == Horizontal {
		measure = l.list.Size().Height
		minItemMeasure = l.list.itemMin.Width
	}

	off, minItem := l.calculateVisibleItemMeasures(minItemMeasure, length, th)
	l.list.propertyLock.Unlock()
	if len(l.visibleItemMeasures) == 0 && length > 0 { // we can't show anything until we have some dimensions
		l.renderLock.Unlock() // user code should not be locked
		return
	}

	oldVisibleLen := len(l.visible)
	l.visible = l.visible[:0]
	oldChildrenLen := len(l.children)
	l.children = l.children[:0]

	axis := off
	for index, itemMeasure := range l.visibleItemMeasures {
		item := index + minItem
		size := fyne.NewSize(measure, itemMeasure)
		position := fyne.NewPos(0, axis)
		if l.list.orientation == Horizontal {
			size = fyne.NewSize(itemMeasure, measure)
			position = fyne.NewPos(axis, 0)
		}

		c, ok := l.searchVisible(wasVisible, item)
		if !ok {
			c = l.getItem()
			if c == nil {
				continue
			}
			c.Resize(size)
		}

		c.Move(position)
		c.Resize(size)

		axis += itemMeasure + separatorThickness
		l.visible = append(l.visible, listItemAndID{id: item, item: c})
		l.children = append(l.children, c)
	}
	l.nilOldSliceData(l.children, len(l.children), oldChildrenLen)
	l.nilOldVisibleSliceData(l.visible, len(l.visible), oldVisibleLen)

	for _, wasVis := range wasVisible {
		if _, ok := l.searchVisible(l.visible, wasVis.id); !ok {
			l.itemPool.Release(wasVis.item)
		}
	}

	l.updateSeparators()

	c := l.list.scroller.Content.(*fyne.Container)
	oldObjLen := len(c.Objects)
	c.Objects = c.Objects[:0]
	c.Objects = append(c.Objects, l.children...)
	c.Objects = append(c.Objects, l.separators...)
	l.nilOldSliceData(c.Objects, len(c.Objects), oldObjLen)

	// make a local deep copy of l.visible since rest of this function is unlocked
	// and cannot safely access l.visible
	visiblePtr := l.slicePool.Get().(*[]listItemAndID)
	visible := (*visiblePtr)[:0]
	visible = append(visible, l.visible...)
	l.renderLock.Unlock() // user code should not be locked

	if newOnly {
		for _, vis := range visible {
			if _, ok := l.searchVisible(wasVisible, vis.id); !ok {
				l.setupListItem(vis.item, vis.id, l.list.focused && l.list.currentFocus == vis.id)
			}
		}
	} else {
		for _, vis := range visible {
			l.setupListItem(vis.item, vis.id, l.list.focused && l.list.currentFocus == vis.id)
		}
	}

	// nil out all references before returning slices to pool
	for i := 0; i < len(wasVisible); i++ {
		wasVisible[i].item = nil
	}
	for i := 0; i < len(visible); i++ {
		visible[i].item = nil
	}
	*wasVisiblePtr = wasVisible // Copy the stack header over to the heap
	*visiblePtr = visible
	l.slicePool.Put(wasVisiblePtr)
	l.slicePool.Put(visiblePtr)
}

func (l *listLayout) updateSeparators() {
	if l.list.HideSeparators {
		l.separators = nil
		return
	}
	if lenChildren := len(l.children); lenChildren > 1 {
		if lenSep := len(l.separators); lenSep > lenChildren {
			l.separators = l.separators[:lenChildren]
		} else {
			for i := lenSep; i < lenChildren; i++ {

				sep := NewSeparator()
				if cache.OverrideThemeMatchingScope(sep, l.list) {
					sep.Refresh()
				}

				l.separators = append(l.separators, sep)
			}
		}
	} else {
		l.separators = nil
	}

	th := l.list.Theme()
	separatorThickness := th.Size(theme.SizeNameSeparatorThickness)
	dividerOff := (th.Size(theme.SizeNamePadding) + separatorThickness) / 2
	for i, child := range l.children {
		if i == 0 {
			continue
		}
		position := fyne.NewPos(0, child.Position().Y-dividerOff)
		size := fyne.NewSize(l.list.Size().Width, separatorThickness)
		if l.list.orientation == Horizontal {
			position = fyne.NewPos(child.Position().X-dividerOff, 0)
			size = fyne.NewSize(separatorThickness, l.list.Size().Height)
		}

		l.separators[i].Move(position)
		l.separators[i].Resize(size)
		l.separators[i].Show()
	}
}

// invariant: visible is in ascending order of IDs
func (l *listLayout) searchVisible(visible []listItemAndID, id ListItemID) (*listItem, bool) {
	ln := len(visible)
	idx := sort.Search(ln, func(i int) bool { return visible[i].id >= id })
	if idx < ln && visible[idx].id == id {
		return visible[idx].item, true
	}
	return nil, false
}

func (l *listLayout) nilOldSliceData(objs []fyne.CanvasObject, len, oldLen int) {
	if oldLen > len {
		objs = objs[:oldLen] // gain view into old data
		for i := len; i < oldLen; i++ {
			objs[i] = nil
		}
	}
}

func (l *listLayout) nilOldVisibleSliceData(objs []listItemAndID, len, oldLen int) {
	if oldLen > len {
		objs = objs[:oldLen] // gain view into old data
		for i := len; i < oldLen; i++ {
			objs[i].item = nil
		}
	}
}

func createItemAndApplyThemeScope(f func() fyne.CanvasObject, scope fyne.Widget) fyne.CanvasObject {
	item := f()
	if !cache.OverrideThemeMatchingScope(item, scope) {
		return item
	}

	item.Refresh()
	return item
}
