import argparse
import json
import configuration as conf
import pandas as pd
import numpy as np
import utils
import os

def main():
    output_path, is_local, prefix = _resolve_args()
    print(f"============= Average experiments =============")
    _process_statistics(output_path, prefix, is_local)

def _process_statistics(output_path, prefix, is_local = True):
    if output_path is None:
        experiment_mode_folder = utils.get_experiment_mode_folder(is_local)
        experiment_path = f"{conf.EXPERIMENT_OUTPUT_FOLDER}/{experiment_mode_folder}/"
        
        if prefix is None:
            for dir in utils.get_experiments_directories(is_local):
                output_path = experiment_path + dir
                _read_experiments_and_calculate_statistics(output_path)
        else:
            output_path = experiment_path + f"average_{prefix}"
            total_metrics_data, metrics_data, aggregated_data = utils.read_experiments_by_prefix(prefix, is_local)

            if not os.path.exists(output_path):
                os.makedirs(output_path, exist_ok=True)
                
            _calculate_statistics(total_metrics_data, metrics_data, aggregated_data, output_path)
    else:
        _read_experiments_and_calculate_statistics(output_path)

def _read_experiments_and_calculate_statistics(output_path):
    total_metrics_data, metrics_data, aggregated_data = utils.read_experiments_file(output_path)
    _calculate_statistics(total_metrics_data, metrics_data, aggregated_data, output_path)

def _calculate_statistics(total_metrics_data, metrics_data, aggregated_data, output_path):

    output = {}

    total_metrics_mean_path, total_metrics_std_path = _calculate_total_metrics_statistics(total_metrics_data, output_path)
    metrics_mean_path, metrics_std_path = _calculate_metrics_statistics(metrics_data, output_path)

    stats = _calculate_aggregated_statistics(aggregated_data)

    file_paths = {
        "total_metrics_mean_path": total_metrics_mean_path,
        "total_metrics_std_path": total_metrics_std_path,
        "metrics_mean_path": metrics_mean_path,
        "metrics_std_path": metrics_std_path
    }

    output = {**file_paths, **stats}

    output_file_path = f"{output_path}/{conf.AVERAGE_EXPERIMENTS_FILE_NAME}"

    with open(output_file_path, 'w') as f:
        json.dump(output, f, indent=2)
    
    print(f"Saved final average experiments file to [{output_file_path}]!")

def _calculate_total_metrics_statistics(metrics: pd.DataFrame, output_path:str):
    properties_to_exclude = [
        "run",
        "execution_time",
        "accuracy"
    ]

    mean_properties = {}
    std_properties = {}

    metrics_mean_path = f"{output_path}/average_total_metrics_mean.csv"
    metrics_std_path = f"{output_path}/average_total_metrics_std.csv"

    for property_name in metrics.columns:
        if property_name in properties_to_exclude:
            continue

        mean_properties[property_name] = metrics[property_name].mean()
        std_properties[property_name] = metrics[property_name].std()

    pd.DataFrame([mean_properties]).to_csv(metrics_mean_path, index=False)
    print(f"Saved metrics means file to [{metrics_mean_path}]!")
    pd.DataFrame([std_properties]).to_csv(metrics_std_path, index=False)
    print(f"Saved metrics std file to [{metrics_std_path}]!")

    return metrics_mean_path, metrics_std_path

def _calculate_metrics_statistics(metrics: pd.DataFrame, output_path:str):
    metrics = metrics.filter(like='_energy')

    metrics_mean = metrics.groupby(metrics.index).mean(numeric_only=True)
    metrics_std = metrics.groupby(metrics.index).std(numeric_only=True)

    metrics_mean_path = f"{output_path}/average_metrics_mean.csv"
    metrics_std_path = f"{output_path}/average_metrics_std.csv"

    metrics_mean.to_csv(metrics_mean_path, index=False)
    print(f"Saved metrics means file to [{metrics_mean_path}]!")
    metrics_std.to_csv(metrics_std_path, index=False)
    print(f"Saved metrics std file to [{metrics_mean_path}]!")

    return metrics_mean_path, metrics_std_path

def _calculate_aggregated_statistics(aggregated_data: dict) -> dict:
    stats = {}
    
    for key, value in aggregated_data.items():
        if isinstance(value, dict):
            stats[key] = {}
            for component, val_list in value.items():
                stats[key][component] = {
                    "mean": float(np.mean(val_list)),
                    "std": float(np.std(val_list))
                }
                
        elif isinstance(value, list) and len(value) > 0:
            first_val = value[0]
            
            if isinstance(first_val, (int, float)):
                stats[key] = {
                    "mean": float(np.mean(value)),
                    "std": float(np.std(value))
                }
                
            elif isinstance(first_val, str) and ":" in first_val:
                sec_list = [pd.to_timedelta(t).total_seconds() for t in value]
                stats[key] = {
                    "mean_seconds": float(np.mean(sec_list)),
                    "std_seconds": float(np.std(sec_list))
                }
                
    return stats

def _resolve_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("-ep", "--experiment-path")
    parser.add_argument("-pr", "--prefix")
    utils.add_boolean_argument(parser, conf.F_ARGUMENT)
    
    args = parser.parse_args()
    is_local = not args.fabric_mode
    prefix_arg = args.prefix

    return utils.resolve_experiment_path(args.experiment_path, is_local, False), is_local, prefix_arg

if __name__ == '__main__':
    main()