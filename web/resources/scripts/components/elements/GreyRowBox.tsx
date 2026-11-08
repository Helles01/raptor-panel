import styled from 'styled-components/macro';
import tw from 'twin.macro';

export default styled.div<{ $hoverable?: boolean }>`
    ${tw`flex rounded no-underline text-neutral-200 items-center bg-surface p-4 border border-line transition-colors duration-150 overflow-hidden`};

    ${(props) => props.$hoverable !== false && tw`hover:border-neutral-600 hover:bg-neutral-700/40`};

    & .icon {
        ${tw`rounded-md w-14 flex items-center justify-center bg-neutral-700 text-neutral-300 p-3`};
    }
`;
