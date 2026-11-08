import React from 'react';
import PageContentBlock from '@/components/elements/PageContentBlock';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import {
    faArrowLeft,
    faCompass,
    faExclamationTriangle,
    faSyncAlt,
    IconDefinition,
} from '@fortawesome/free-solid-svg-icons';
import tw from 'twin.macro';
import Button from '@/components/elements/Button';

interface BaseProps {
    title: string;
    icon: IconDefinition;
    message: string;
    onRetry?: () => void;
    onBack?: () => void;
}

interface PropsWithRetry extends BaseProps {
    onRetry?: () => void;
    onBack?: never;
}

interface PropsWithBack extends BaseProps {
    onBack?: () => void;
    onRetry?: never;
}

export type ScreenBlockProps = PropsWithBack | PropsWithRetry;

// A whole page that says one thing: what happened, and a way out when there is one.
const ScreenBlock = ({ title, icon, message, onBack, onRetry }: ScreenBlockProps) => (
    <PageContentBlock>
        <div css={tw`flex justify-center`}>
            <div css={tw`w-full max-w-lg rounded-xl bg-surface border border-line px-8 py-12 text-center`}>
                <div
                    css={tw`w-12 h-12 mx-auto flex items-center justify-center rounded-lg bg-neutral-700 text-ember-400`}
                >
                    <FontAwesomeIcon icon={icon} size={'lg'} />
                </div>
                <h2 css={tw`mt-6 text-neutral-50 text-2xl`}>{title}</h2>
                <p css={tw`mt-2 text-sm text-neutral-300`}>{message}</p>
                {typeof onRetry === 'function' && (
                    <Button color={'grey'} css={tw`mt-6`} onClick={() => onRetry()}>
                        <FontAwesomeIcon icon={faSyncAlt} css={tw`mr-2`} />
                        Try again
                    </Button>
                )}
                {typeof onBack === 'function' && (
                    <Button color={'grey'} css={tw`mt-6`} onClick={() => onBack()}>
                        <FontAwesomeIcon icon={faArrowLeft} css={tw`mr-2`} />
                        Go back
                    </Button>
                )}
            </div>
        </div>
    </PageContentBlock>
);

type ServerErrorProps = (Omit<PropsWithBack, 'icon' | 'title'> | Omit<PropsWithRetry, 'icon' | 'title'>) & {
    title?: string;
};

const ServerError = ({ title, ...props }: ServerErrorProps) => (
    <ScreenBlock title={title || 'Something went wrong'} icon={faExclamationTriangle} {...props} />
);

const NotFound = ({ title, message, onBack }: Partial<Pick<ScreenBlockProps, 'title' | 'message' | 'onBack'>>) => (
    <ScreenBlock
        title={title || 'Not found'}
        icon={faCompass}
        message={message || 'The requested resource was not found.'}
        onBack={onBack}
    />
);

export { ServerError, NotFound };
export default ScreenBlock;
