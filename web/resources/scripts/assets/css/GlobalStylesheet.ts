import tw from 'twin.macro';
import { createGlobalStyle } from 'styled-components/macro';
// @ts-expect-error untyped font file
import geistLatin from '@fontsource-variable/geist/files/geist-latin-wght-normal.woff2';
// @ts-expect-error untyped font file
import geistLatinExt from '@fontsource-variable/geist/files/geist-latin-ext-wght-normal.woff2';
// @ts-expect-error untyped font file
import monoLatin from '@fontsource-variable/geist-mono/files/geist-mono-latin-wght-normal.woff2';
// @ts-expect-error untyped font file
import monoLatinExt from '@fontsource-variable/geist-mono/files/geist-mono-latin-ext-wght-normal.woff2';

const latin =
    'U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD';
const latinExt =
    'U+0100-02BA,U+02BD-02C5,U+02C7-02CC,U+02CE-02D7,U+02DD-02FF,U+0304,U+0308,U+0329,U+1D00-1DBF,U+1E00-1E9F,U+1EF2-1EFF,U+2020,U+20A0-20AB,U+20AD-20C0,U+2113,U+2C60-2C7F,U+A720-A7FF';

export default createGlobalStyle`
    @font-face {
        font-family: 'Geist Variable';
        font-style: normal;
        font-display: swap;
        font-weight: 100 900;
        src: url(${geistLatinExt}) format('woff2-variations');
        unicode-range: ${latinExt};
    }

    @font-face {
        font-family: 'Geist Variable';
        font-style: normal;
        font-display: swap;
        font-weight: 100 900;
        src: url(${geistLatin}) format('woff2-variations');
        unicode-range: ${latin};
    }

    @font-face {
        font-family: 'Geist Mono Variable';
        font-style: normal;
        font-display: swap;
        font-weight: 100 900;
        src: url(${monoLatinExt}) format('woff2-variations');
        unicode-range: ${latinExt};
    }

    @font-face {
        font-family: 'Geist Mono Variable';
        font-style: normal;
        font-display: swap;
        font-weight: 100 900;
        src: url(${monoLatin}) format('woff2-variations');
        unicode-range: ${latin};
    }

    body {
        ${tw`font-sans bg-canvas text-neutral-100`};
        -webkit-font-smoothing: antialiased;
        text-rendering: optimizeLegibility;
    }

    h1, h2, h3, h4, h5, h6 {
        ${tw`font-semibold font-header`};
        letter-spacing: -0.015em;
    }

    p {
        ${tw`text-neutral-200 leading-snug font-sans`};
    }

    /* Numbers line up in columns, and what is code or a console is set in the monospace. */
    code, pre, kbd, samp {
        ${tw`font-mono`};
    }

    table, .tabular {
        font-variant-numeric: tabular-nums;
    }

    form {
        ${tw`m-0`};
    }

    textarea, select, input, button, button:focus, button:focus-visible {
        ${tw`outline-none`};
    }

    /* A ring that shows where the keyboard is, on everything that can be reached with it. */
    a:focus-visible, button:focus-visible, [role=button]:focus-visible, summary:focus-visible {
        outline: 2px solid ${'#e4834f'};
        outline-offset: 2px;
    }

    ::selection {
        background: rgb(224 112 58 / 35%);
    }

    input[type=number]::-webkit-outer-spin-button,
    input[type=number]::-webkit-inner-spin-button {
        -webkit-appearance: none !important;
        margin: 0;
    }

    input[type=number] {
        -moz-appearance: textfield !important;
    }

    /* Scroll Bar Style */
    ::-webkit-scrollbar {
        background: none;
        width: 14px;
        height: 14px;
    }

    ::-webkit-scrollbar-thumb {
        border: solid 0 rgb(0 0 0 / 0%);
        border-right-width: 4px;
        border-left-width: 4px;
        -webkit-border-radius: 6px;
        -webkit-box-shadow: inset 0 0 0 4px #3a3d42;
    }

    ::-webkit-scrollbar-thumb:hover {
        -webkit-box-shadow: inset 0 0 0 4px #4d5157;
    }

    ::-webkit-scrollbar-track-piece {
        margin: 4px 0;
    }

    ::-webkit-scrollbar-thumb:horizontal {
        border-right-width: 0;
        border-left-width: 0;
        border-top-width: 4px;
        border-bottom-width: 4px;
    }

    ::-webkit-scrollbar-corner {
        background: transparent;
    }

    @media (prefers-reduced-motion: reduce) {
        *, *::before, *::after {
            animation-duration: 0.01ms !important;
            animation-iteration-count: 1 !important;
            transition-duration: 0.01ms !important;
        }
    }
`;
