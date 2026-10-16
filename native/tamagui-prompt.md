# Tamagui Configuration

This document provides an overview of the Tamagui configuration for this project.

## Configuration Settings

**IMPORTANT:** These settings affect how you write Tamagui code in this project.

### Default Font: `body`

All text components will use the "body" font family by default.

### Only Allow Shorthands: `true`

**You MUST use shorthand properties in this project.**

Full property names are not allowed. For example:
- ✅ `<View w="10" />` (correct)
- ❌ `<View width="10" />` (will error)

See the Shorthand Properties section below for all available shorthands.

### Style value syntax

Both string and object style value syntax are allowed.

### Theme Class Name: `html`

Theme classes are applied to the root HTML element.

### Allowed Style Values

Type validation: `"somewhat-strict-web"`.

Single-token values are type-checked. Run `tamagui check --strict` to also validate conditional payloads.

## Flat Value Grammar

Conditional style values follow this grammar:

```txt
value  := base? clause*
clause := modifier(:modifier)*:payload
```

- No `$` sigils: bare token names (`bg="background"`, not `bg="$background"`).
- Kebab-case theme names: `background-hover`, `border-color`, `shadow-color`.
- Numbers are px: `p={4}` is 4px, while `p="4"` is space token 4.
- Specificity precedence: platform (`ios:` > `native:` > bare) > condition count > category (media < container < theme < group < state).

Both forms are allowed:

```tsx
// string form
<View bg="background hover:background-hover dark:blue-500" p="4 sm:6 max-sm:2" />

// object form
<View bg={{ default: 'background', hover: 'background-hover', dark: 'blue-500' }} p={{ default: '4', sm: '6', 'max-sm': '2' }} />
```

## Shorthand Properties

These shorthand properties are available for styling:

- `b` → `bottom`
- `bg` → `background`
- `content` → `alignContent`
- `grow` → `flexGrow`
- `h` → `height`
- `items` → `alignItems`
- `justify` → `justifyContent`
- `l` → `left`
- `m` → `margin`
- `maxH` → `maxHeight`
- `maxW` → `maxWidth`
- `mb` → `marginBottom`
- `minH` → `minHeight`
- `minW` → `minWidth`
- `ml` → `marginLeft`
- `mr` → `marginRight`
- `mt` → `marginTop`
- `mx` → `marginHorizontal`
- `my` → `marginVertical`
- `p` → `padding`
- `pb` → `paddingBottom`
- `pl` → `paddingLeft`
- `pr` → `paddingRight`
- `pt` → `paddingTop`
- `px` → `paddingHorizontal`
- `py` → `paddingVertical`
- `r` → `right`
- `rounded` → `borderRadius`
- `select` → `userSelect`
- `self` → `alignSelf`
- `shrink` → `flexShrink`
- `t` → `top`
- `text` → `textAlign`
- `w` → `width`
- `z` → `zIndex`

## Themes

- `dark`
- `dark_accent`
- `dark_accent_level2`
- `dark_accent_level2_level2`
- `dark_accent_level2_level3`
- `dark_accent_level2_level4`
- `dark_accent_level3`
- `dark_accent_level4`
- `dark_active`
- `dark_active_level2`
- `dark_active_level2_level2`
- `dark_active_level2_level2_level2`
- `dark_active_level2_level2_level3`
- `dark_active_level2_level2_level4`
- `dark_active_level2_level3`
- `dark_active_level2_level4`
- `dark_active_level3`
- `dark_active_level3_level2`
- `dark_active_level3_level3`
- `dark_active_level3_level4`
- `dark_active_level4`
- `dark_black`
- `dark_black_level2`
- `dark_black_level2_level2`
- `dark_black_level2_level2_level2`
- `dark_black_level2_level2_level3`
- `dark_black_level2_level2_level4`
- `dark_black_level2_level3`
- `dark_black_level2_level4`
- `dark_black_level3`
- `dark_black_level3_level2`
- `dark_black_level3_level3`
- `dark_black_level3_level4`
- `dark_black_level4`
- `dark_brand`
- `dark_brand_level2`
- `dark_brand_level2_level2`
- `dark_brand_level2_level2_level2`
- `dark_brand_level2_level2_level3`
- `dark_brand_level2_level2_level4`
- `dark_brand_level2_level3`
- `dark_brand_level2_level4`
- `dark_brand_level3`
- `dark_brand_level3_level2`
- `dark_brand_level3_level3`
- `dark_brand_level3_level4`
- `dark_brand_level4`
- `dark_green`
- `dark_green_level2`
- `dark_green_level2_level2`
- `dark_green_level2_level3`
- `dark_green_level2_level4`
- `dark_green_level3`
- `dark_green_level4`
- `dark_inverse`
- `dark_inverse_level2`
- `dark_inverse_level2_level2`
- `dark_inverse_level2_level2_level2`
- `dark_inverse_level2_level2_level3`
- `dark_inverse_level2_level2_level4`
- `dark_inverse_level2_level3`
- `dark_inverse_level2_level4`
- `dark_inverse_level3`
- `dark_inverse_level3_level2`
- `dark_inverse_level3_level3`
- `dark_inverse_level3_level4`
- `dark_inverse_level4`
- `dark_level2`
- `dark_level2_level2`
- `dark_level2_level2_level2`
- `dark_level2_level2_level3`
- `dark_level2_level2_level4`
- `dark_level2_level3`
- `dark_level2_level4`
- `dark_level3`
- `dark_level3_level2`
- `dark_level3_level3`
- `dark_level3_level4`
- `dark_level4`
- `dark_red`
- `dark_red_level2`
- `dark_red_level2_level2`
- `dark_red_level2_level3`
- `dark_red_level2_level4`
- `dark_red_level3`
- `dark_red_level4`
- `dark_white`
- `dark_white_level2`
- `dark_white_level2_level2`
- `dark_white_level2_level2_level2`
- `dark_white_level2_level2_level3`
- `dark_white_level2_level2_level4`
- `dark_white_level2_level3`
- `dark_white_level2_level4`
- `dark_white_level3`
- `dark_white_level3_level2`
- `dark_white_level3_level3`
- `dark_white_level3_level4`
- `dark_white_level4`
- `dark_yellow`
- `dark_yellow_level2`
- `dark_yellow_level2_level2`
- `dark_yellow_level2_level3`
- `dark_yellow_level2_level4`
- `dark_yellow_level3`
- `dark_yellow_level4`
- `light`
- `light_accent`
- `light_accent_level2`
- `light_accent_level2_level2`
- `light_accent_level2_level3`
- `light_accent_level2_level4`
- `light_accent_level3`
- `light_accent_level4`
- `light_active`
- `light_active_level2`
- `light_active_level2_level2`
- `light_active_level2_level2_level2`
- `light_active_level2_level2_level3`
- `light_active_level2_level2_level4`
- `light_active_level2_level3`
- `light_active_level2_level4`
- `light_active_level3`
- `light_active_level3_level2`
- `light_active_level3_level3`
- `light_active_level3_level4`
- `light_active_level4`
- `light_black`
- `light_black_level2`
- `light_black_level2_level2`
- `light_black_level2_level2_level2`
- `light_black_level2_level2_level3`
- `light_black_level2_level2_level4`
- `light_black_level2_level3`
- `light_black_level2_level4`
- `light_black_level3`
- `light_black_level3_level2`
- `light_black_level3_level3`
- `light_black_level3_level4`
- `light_black_level4`
- `light_brand`
- `light_brand_level2`
- `light_brand_level2_level2`
- `light_brand_level2_level2_level2`
- `light_brand_level2_level2_level3`
- `light_brand_level2_level2_level4`
- `light_brand_level2_level3`
- `light_brand_level2_level4`
- `light_brand_level3`
- `light_brand_level3_level2`
- `light_brand_level3_level3`
- `light_brand_level3_level4`
- `light_brand_level4`
- `light_green`
- `light_green_level2`
- `light_green_level2_level2`
- `light_green_level2_level3`
- `light_green_level2_level4`
- `light_green_level3`
- `light_green_level4`
- `light_inverse`
- `light_inverse_level2`
- `light_inverse_level2_level2`
- `light_inverse_level2_level2_level2`
- `light_inverse_level2_level2_level3`
- `light_inverse_level2_level2_level4`
- `light_inverse_level2_level3`
- `light_inverse_level2_level4`
- `light_inverse_level3`
- `light_inverse_level3_level2`
- `light_inverse_level3_level3`
- `light_inverse_level3_level4`
- `light_inverse_level4`
- `light_level2`
- `light_level2_level2`
- `light_level2_level2_level2`
- `light_level2_level2_level3`
- `light_level2_level2_level4`
- `light_level2_level3`
- `light_level2_level4`
- `light_level3`
- `light_level3_level2`
- `light_level3_level3`
- `light_level3_level4`
- `light_level4`
- `light_red`
- `light_red_level2`
- `light_red_level2_level2`
- `light_red_level2_level3`
- `light_red_level2_level4`
- `light_red_level3`
- `light_red_level4`
- `light_white`
- `light_white_level2`
- `light_white_level2_level2`
- `light_white_level2_level2_level2`
- `light_white_level2_level2_level3`
- `light_white_level2_level2_level4`
- `light_white_level2_level3`
- `light_white_level2_level4`
- `light_white_level3`
- `light_white_level3_level2`
- `light_white_level3_level3`
- `light_white_level3_level4`
- `light_white_level4`
- `light_yellow`
- `light_yellow_level2`
- `light_yellow_level2_level2`
- `light_yellow_level2_level3`
- `light_yellow_level2_level4`
- `light_yellow_level3`
- `light_yellow_level4`

Theme names above are exact configured names. Nested themes resolve relative to their parent. Use an explicit theme boundary in a component skin.

### Theme Usage

```tsx
<Theme name="dark">
  <Button>Uses this theme</Button>
</Theme>
```

**Accessing theme values:**

Components access theme values by their bare names:

```tsx
// string form
<View bg="background hover:background-hover" color="color" />

// object form
<View bg={{ default: 'background', hover: 'background-hover' }} color={{ default: 'color' }} />
```

**Special props:**

- `theme="inverse"`: Uses the opposite light or dark sub-theme
## Tokens

Tokens are design system values referenced by their bare names.

### Space Tokens

- `-96`: -384
- `-80`: -320
- `-72`: -288
- `-64`: -256
- `-60`: -240
- `-56`: -224
- `-52`: -208
- `-48`: -192
- `-44`: -176
- `-40`: -160
- `-36`: -144
- `-32`: -128
- `-28`: -112
- `-24`: -96
- `-20`: -80
- `-16`: -64
- `-14`: -56
- `-12`: -48
- `-11`: -44
- `-10`: -40
- `-9`: -36
- `-8`: -32
- `-7`: -28
- `-6`: -24
- `-5`: -20
- `-4`: -16
- `-3.5`: -14
- `-3`: -12
- `-3-5`: -14
- `-2.5`: -10
- `-2`: -8
- `-2-5`: -10
- `-1.5`: -6
- `-1`: -4
- `-1-5`: -6
- `-0.5`: -2
- `-0-5`: -2
- `-px`: -1
- `0`: 0
- `0-5`: 2
- `0.5`: 2
- `1`: 4
- `1-5`: 6
- `1.5`: 6
- `2`: 8
- `2-5`: 10
- `2.5`: 10
- `3`: 12
- `3-5`: 14
- `3.5`: 14
- `4`: 16
- `5`: 20
- `6`: 24
- `7`: 28
- `8`: 32
- `9`: 36
- `10`: 40
- `11`: 44
- `12`: 48
- `14`: 56
- `16`: 64
- `20`: 80
- `24`: 96
- `28`: 112
- `32`: 128
- `36`: 144
- `40`: 160
- `44`: 176
- `48`: 192
- `52`: 208
- `56`: 224
- `60`: 240
- `64`: 256
- `72`: 288
- `80`: 320
- `96`: 384
- `px`: 1

### Size Tokens

- `0`: 0
- `0-5`: 2
- `0.5`: 2
- `1`: 4
- `1-5`: 6
- `1.5`: 6
- `2`: 8
- `2-5`: 10
- `2.5`: 10
- `3`: 12
- `3-5`: 14
- `3.5`: 14
- `4`: 16
- `5`: 20
- `6`: 24
- `7`: 28
- `8`: 32
- `9`: 36
- `10`: 40
- `11`: 44
- `12`: 48
- `14`: 56
- `16`: 64
- `20`: 80
- `24`: 96
- `28`: 112
- `32`: 128
- `36`: 144
- `40`: 160
- `44`: 176
- `48`: 192
- `52`: 208
- `56`: 224
- `60`: 240
- `64`: 256
- `72`: 288
- `80`: 320
- `96`: 384
- `px`: 1

### Radius Tokens

- `0`: 0
- `1`: 3
- `2`: 5
- `2xl`: 16
- `3`: 7
- `3xl`: 24
- `4`: 9
- `4xl`: 32
- `5`: 10
- `6`: 16
- `7`: 19
- `8`: 22
- `9`: 26
- `10`: 34
- `11`: 42
- `12`: 50
- `full`: 9999
- `lg`: 8
- `md`: 6
- `sm`: 4
- `xl`: 12
- `xs`: 2

### Color Tokens

**Tailwind Palettes (`<name>-<50..950>`):** amber, blue, brand, cyan, emerald, fuchsia, gray, green, indigo, lime, mauve, mist, neutral, olive, orange, pink, purple, red, rose, sky, slate, stone, taupe, teal, violet, yellow, zinc

**Named Colors:**

- `black`: #000000
- `white`: #ffffff

### Token Usage

Tokens can be used in component props by their bare names:

```tsx
// String form
<View p="4 sm:6" w="10 sm:12" bg="blue-100" />

// Object form
<View p={{ default: '4', sm: '6' }} w={{ default: '10', sm: '12' }} bg={{ default: 'blue-100' }} />

// Space and radius tokens
<View gap="2" m="3" h="6" rounded="md" />
```

## Media Queries

Available responsive breakpoints:

- **height-lg**: min-height: 1024px (screens >= 1024px tall)
- **height-md**: min-height: 768px (screens >= 768px tall)
- **height-sm**: min-height: 640px (screens >= 640px tall)
- **hoverable**: pointer: hover (hover)
- **lg**: min-width: 1024px (screens >= 1024px wide)
- **max-100**: max-width: 99.98px (screens <= 99.98px wide)
- **max-200**: max-width: 199.98px (screens <= 199.98px wide)
- **max-height-100**: max-height: 99.98px (screens <= 99.98px tall)
- **max-height-200**: max-height: 199.98px (screens <= 199.98px tall)
- **max-height-lg**: max-height: 1023.98px (screens <= 1023.98px tall)
- **max-height-md**: max-height: 767.98px (screens <= 767.98px tall)
- **max-height-sm**: max-height: 639.98px (screens <= 639.98px tall)
- **max-height-xs**: max-height: 459.98px (screens <= 459.98px tall)
- **max-height-xxs**: max-height: 339.98px (screens <= 339.98px tall)
- **max-height-xxxs**: max-height: 259.98px (screens <= 259.98px tall)
- **max-lg**: max-width: 1023.98px (screens <= 1023.98px wide)
- **max-md**: max-width: 767.98px (screens <= 767.98px wide)
- **max-sm**: max-width: 639.98px (screens <= 639.98px wide)
- **max-xl**: max-width: 1279.98px (screens <= 1279.98px wide)
- **max-xs**: max-width: 459.98px (screens <= 459.98px wide)
- **max-xxl**: max-width: 1535.98px (screens <= 1535.98px wide)
- **max-xxs**: max-width: 339.98px (screens <= 339.98px wide)
- **max-xxxs**: max-width: 259.98px (screens <= 259.98px wide)
- **md**: min-width: 768px (screens >= 768px wide)
- **sm**: min-width: 640px (screens >= 640px wide)
- **touchable**: pointer: coarse
- **xl**: min-width: 1280px (screens >= 1280px wide)
- **xs**: min-width: 460px (screens >= 460px wide)
- **xxl**: min-width: 1536px (screens >= 1536px wide)
- **xxs**: min-width: 340px (screens >= 340px wide)
- **xxxs**: min-width: 260px (screens >= 260px wide)

### Media Query Usage

Media queries can be used as style props or with the `useMedia` hook:

```tsx
// String form and object form
<View w="100% md:50%" />
<View w={{ default: '100%', 'md': '50%' }} />

// Using the useMedia hook
const media = useMedia()
if (media.md) {
  // Render for this breakpoint
}
```

## Fonts

Available font families:

- body
- heading

## Animations

Available animation presets:

- bouncy
- lazy
- medium
- quick
- quickLessBouncy
- quicker
- quickerLessBouncy
- quickest
- quickestLessBouncy
- slow
- slowest
- superBouncy
- superLazy

## Components

Available named exports (import these names directly):

- ACTIONS
- Accordion
- Adapt
- AdaptCapabilities
- AdaptContents
- AdaptParent
- AdaptPortalContents
- AlertDialog
- AlertDialogAction
- AlertDialogCancel
- AlertDialogContent
- AlertDialogDescription
- AlertDialogDestructive
- AlertDialogOverlay
- AlertDialogPortal
- AlertDialogTitle
- AlertDialogTrigger
- Anchor
- Animate
- AnimatePresence
- Avatar
- AvatarFallback
- AvatarImage
- Button
- ButtonIcon
- ButtonText
- Card
- CardBackground
- CardFooter
- CardHeader
- Checkbox
- CheckboxIndicator
- Circle
- ClientOnly
- Collapsible
- CollapsibleContent
- CollapsibleTrigger
- Configuration
- ContextMenu
- Dialog
- DialogClose
- DialogContent
- DialogDescription
- DialogOverlay
- DialogPortal
- DialogTitle
- DialogTrigger
- Em
- EnsureFlexed
- Field
- FieldDescriptionFrame
- FieldErrorFrame
- FieldItemFrame
- FieldLabelFrame
- Fieldset
- FontLanguage
- Form
- FormTrigger
- Group
- H1
- H2
- H3
- H4
- H5
- H6
- Heading
- INITIAL_STATE
- Image
- Input
- Label
- ListItem
- ListItemSubtitle
- ListItemText
- ListItemTitle
- Menu
- Paragraph
- Popover
- PopoverAnchor
- PopoverArrow
- PopoverClose
- PopoverContent
- PopoverTrigger
- Popper
- PopperAnchor
- PopperArrow
- PopperContent
- PopperContextFast
- PopperContextSlow
- PopperProviderFast
- PopperProviderSlow
- Portal
- PortalHost
- PortalItem
- PresenceChild
- Progress
- ProgressIndicator
- RadioGroup
- RadioGroupIndicator
- RadioGroupItem
- Range
- ResetPresence
- ScrollView
- Select
- SelectGroup
- SelectIcon
- SelectIndicator
- SelectItem
- SelectItemIndicator
- SelectItemText
- SelectLabel
- SelectRoot
- SelectScrollDownButton
- SelectScrollUpButton
- SelectSeparator
- SelectTrigger
- SelectValue
- SelectViewport
- Separator
- Sheet
- SheetBackground
- SheetContainer
- SheetControlled
- SheetController
- SheetHandle
- SheetOverlay
- SheetRoot
- SheetScrollView
- SizableText
- Slider
- SliderActive
- SliderThumb
- SliderTrack
- Spacer
- Span
- Spinner
- Square
- Strong
- Surface
- Switch
- SwitchThumb
- Tabs
- TabsContent
- TabsList
- TabsTab
- Text
- TextArea
- Theme
- ThemeUpdate
- Thumb
- Toast
- ToastAction
- ToastClose
- ToastDescription
- ToastItem
- ToastTitle
- ToggleGroup
- Tooltip
- TooltipGroup
- TooltipSimple
- Track
- View
- VisuallyHidden
- XGroup
- XStack
- YGroup
- YStack
- ZStack

