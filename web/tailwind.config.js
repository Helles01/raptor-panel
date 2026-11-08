// Raptor's look: graphite surfaces and one ember accent. The names are the ones the components already use (gray and neutral from 50, the lightest, to 900,
// the darkest; blue, cyan and primary for the accent), so changing the values here changes every screen at once.

// Cool, almost colourless greys. 100 is the text, 300 the supporting text, 500 the muted text (4.6:1 on the canvas), 700 and 800 the surfaces.
const gray = {
    50: '#f4f5f6',
    100: '#e7e8ea',
    200: '#cfd2d6',
    300: '#a8acb2',
    400: '#8e939a',
    500: '#7b8087',
    600: '#484c52',
    700: '#2c2f33',
    800: '#1b1c1e',
    900: '#121314',
};

// The one accent, a burnt orange kept below 70% saturation. 500 is the fill of a primary button and 400 the colour of links and focus rings.
const ember = {
    50: '#fdf1ea',
    100: '#fadfd0',
    200: '#f4c0a3',
    300: '#ec9d70',
    400: '#e4834f',
    500: '#e0703a',
    600: '#c25a2a',
    700: '#9c4622',
    800: '#75351b',
    900: '#4f2412',
};

// What a state means has its own colours, calmer than Tailwind's.
const green = {
    50: '#edf7f1', 100: '#d4ecdd', 200: '#aedcbf', 300: '#86c9a0', 400: '#63b784',
    500: '#47a06b', 600: '#378253', 700: '#2c6743', 800: '#234f35', 900: '#1a3a27',
};
const red = {
    50: '#fdefee', 100: '#fad9d7', 200: '#f3b1ad', 300: '#ea8a85', 400: '#e06c66',
    500: '#cf514b', 600: '#ad3d38', 700: '#8a302c', 800: '#662522', 900: '#471a18',
};
const yellow = {
    50: '#fbf6e6', 100: '#f5ecc7', 200: '#ecd98f', 300: '#e2c45d', 400: '#d6ae3a',
    500: '#bd9429', 600: '#977420', 700: '#735919', 800: '#544114', 900: '#3a2d0e',
};

module.exports = {
    content: [
        './resources/scripts/**/*.{js,ts,tsx}',
    ],
    theme: {
        extend: {
            fontFamily: {
                sans: ['"Geist Variable"', 'ui-sans-serif', 'system-ui', 'sans-serif'],
                header: ['"Geist Variable"', 'ui-sans-serif', 'system-ui', 'sans-serif'],
                mono: ['"Geist Mono Variable"', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace'],
            },
            colors: {
                black: '#0d0e0f',
                // The page, a raised surface and the hairline between them.
                canvas: '#141516',
                surface: '#1b1c1e',
                line: '#2a2c2f',
                // "primary" and "neutral" are deprecated, prefer the use of "blue" and "gray"
                // in new code. Blue and cyan are the accent as well: there is one.
                primary: ember,
                blue: ember,
                cyan: ember,
                ember,
                gray: gray,
                neutral: gray,
                green,
                red,
                yellow,
            },
            borderRadius: {
                DEFAULT: '8px',
                md: '8px',
                lg: '12px',
                xl: '16px',
            },
            fontSize: {
                '2xs': '0.625rem',
            },
            transitionDuration: {
                250: '250ms',
            },
            borderColor: theme => ({
                default: theme('colors.neutral.400', 'currentColor'),
            }),
        },
    },
    plugins: [
        require('@tailwindcss/line-clamp'),
        require('@tailwindcss/forms')({
            strategy: 'class',
        }),
    ]
};
