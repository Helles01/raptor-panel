import React from 'react';

// The mark of the panel: three claw marks. It takes its colour from the text it sits in.
const Logo = ({ size = 20, className }: { size?: number; className?: string }) => (
    <svg
        width={size}
        height={size}
        viewBox={'0 0 18 18'}
        aria-hidden={'true'}
        className={className}
        focusable={'false'}
    >
        <path
            d={'M3 15 8 3M7 15 12 3M11 15 16 3'}
            stroke={'currentColor'}
            strokeWidth={'2'}
            strokeLinecap={'square'}
            fill={'none'}
        />
    </svg>
);

export default Logo;
